// Player: plays the song at the head of the shared queue.
//
// Audio: every track is downloaded, decoded into an AudioBuffer and played
// through the Web Audio API. All tracks are started at the same instant, so
// they stay sample-accurate with each other; each one goes through its own
// GainNode for volume and mute.
//
// Video and the mixer UI are shared with the practice page (playback.js).
//
// The server's queue is the source of truth: the player polls it, so a skip or
// a new song from any device reaches it on its own.
//
// Only one device plays the queue: the owner. Its poll renews a lease on the
// server and reports what it's playing. Other devices watch that report, can
// ask the owner to pass the player on, and take over once nobody holds it.

const POLL_MS = 3000;
const TICK_MS = 250;
const PREFS_KEY = "minusone.mixer";

let audioCtx = null;
let current = null; // { song, mixer, finishing }
let loadToken = 0;
let seeking = false;
let idle = false; // the empty-queue screen is showing
let isOwner = false; // this device plays the queue
let claimNext = false; // the next poll takes the player if it's free
let lastState = null; // the last poll's answer
let polling = false;

// ---------------------------------------------------------------- audio

class Mixer {
  constructor(ctx) {
    this.ctx = ctx;
    this.tracks = [];
    this.sources = [];
    this.duration = 0;
    this.offset = 0; // position when paused
    this.startedAt = 0; // ctx time at which position 0 would have played
    this.playing = false;
  }

  async load(tracks, onProgress) {
    let loaded = 0;
    const buffers = await Promise.all(
      tracks.map(async (t) => {
        const res = await fetch(t.url);
        if (!res.ok) throw new Error(`${t.label}: HTTP ${res.status}`);
        const buffer = await this.ctx.decodeAudioData(await res.arrayBuffer());
        onProgress(++loaded, tracks.length);
        return buffer;
      }),
    );
    this.tracks = tracks.map((t, i) => {
      const gain = this.ctx.createGain();
      gain.connect(this.ctx.destination);
      return { ...t, buffer: buffers[i], gain };
    });
    this.duration = Math.max(...buffers.map((b) => b.duration));
  }

  setGain(track, value, immediate = false) {
    if (immediate) track.gain.gain.value = value;
    else track.gain.gain.setTargetAtTime(value, this.ctx.currentTime, 0.02);
  }

  play() {
    if (this.playing) return;
    // scheduled slightly ahead so every source starts on the same sample
    const when = this.ctx.currentTime + 0.05;
    this.sources = this.tracks.map((t) => {
      const source = this.ctx.createBufferSource();
      source.buffer = t.buffer;
      source.connect(t.gain);
      source.start(when, this.offset);
      return source;
    });
    this.startedAt = when - this.offset;
    this.playing = true;
  }

  pause() {
    if (!this.playing) return;
    this.offset = this.time();
    this.stopSources();
    this.playing = false;
  }

  seek(seconds) {
    const wasPlaying = this.playing;
    this.pause();
    this.offset = Math.min(Math.max(seconds, 0), this.duration);
    if (wasPlaying) this.play();
  }

  time() {
    if (!this.playing) return this.offset;
    return Math.min(Math.max(this.ctx.currentTime - this.startedAt, 0), this.duration);
  }

  stopSources() {
    for (const source of this.sources) {
      try {
        source.stop();
      } catch {}
      source.disconnect();
    }
    this.sources = [];
  }

  destroy() {
    this.stopSources();
    this.playing = false;
    for (const t of this.tracks) t.gain.disconnect();
    this.tracks = [];
  }
}

// ---------------------------------------------------------------- queue

async function poll() {
  if (polling) return;
  polling = true;
  try {
    await heartbeat();
  } finally {
    polling = false;
  }
}

async function heartbeat() {
  // Watching devices don't take a free player on their own, unless the
  // user just asked to play here.
  const claim = Boolean(audioCtx) && (isOwner || claimNext || lastState?.requested);
  let res;
  try {
    res = await fetch("/player/heartbeat", {
      method: "POST",
      headers: { "Content-Type": "application/json", Accept: "application/json", "X-Device-Id": deviceId() },
      body: JSON.stringify({ claim, status: report() }),
    });
  } catch {
    return; // offline for a moment: keep playing what we have
  }
  if (res.redirected) {
    location.href = "/login"; // session expired
    return;
  }
  if (!res.ok) return;
  claimNext = false;
  applyState(await res.json());
}

// report is what the owner tells the other devices it's doing.
function report() {
  if (!isOwner || !current) return { itemId: null, paused: true, position: 0 };
  const mixer = current.mixer;
  if (!mixer) return { itemId: current.song.itemId, paused: true, position: current.startAt };
  return { itemId: current.song.itemId, paused: !mixer.playing, position: mixer.time() };
}

function applyState(state) {
  lastState = state;
  const wasOwner = isOwner;
  // the server can still count a reloaded tab as the owner, but it can't
  // make sound until it gets a click
  isOwner = state.owner && Boolean(audioCtx);
  $("player").classList.toggle("is-watching", !isOwner);

  if (!isOwner) {
    if (wasOwner) stopPlaying();
    showWatching(state);
    return;
  }

  if (!wasOwner) htmx.trigger(document.body, "queueChanged"); // the list's skip button
  $("start-overlay").hidden = true;
  showRequest(state.requester);
  const song = state.song;
  if (!song) {
    if (!idle) showIdle();
    return;
  }
  if (!current || current.song.itemId !== song.itemId) {
    // taking over: pick the song up where the previous owner left it
    const resume = !wasOwner && state.resume?.itemId === song.itemId ? state.resume : null;
    loadSong(song, resume);
  }
}

async function loadSong(song, resume) {
  const token = ++loadToken;
  idle = false;
  current?.mixer?.destroy();
  current = { song, mixer: null, finishing: false, startAt: resume?.position ?? 0 };

  $("np-title").textContent = song.title || "Untitled";
  $("np-author").textContent = song.author || "";
  $("mixer").hidden = true;
  setControlsEnabled(false);
  $("skip").disabled = false;
  updateTime(0, 0);
  cueVideo(song.videoId);

  if (song.tracks.length === 0) {
    showMessage("This song has no audio. Skip it to continue.");
    return;
  }

  showMessage("Loading audio…");
  const mixer = new Mixer(audioCtx);
  try {
    await mixer.load(song.tracks, (done, total) => {
      if (token === loadToken && total > 1) showMessage(`Loading audio ${done}/${total}…`);
    });
  } catch (err) {
    if (token === loadToken) showMessage(`Couldn't load the audio (${err.message}). Skip it to continue.`);
    return;
  }
  if (token !== loadToken) {
    mixer.destroy(); // another song took over while this one was loading
    return;
  }

  current.mixer = mixer;
  buildMixer(song, mixer, PREFS_KEY);
  hideMessage();
  setControlsEnabled(true);
  $("seek").max = mixer.duration;
  if (resume) mixer.seek(resume.position);
  if (!resume?.paused) mixer.play();
  updatePlayButton();
}

// Removes the current song from the queue; the next poll loads the next one.
async function finish() {
  if (!current || current.finishing) return;
  current.finishing = true;
  current.mixer?.pause();
  updatePlayButton();
  try {
    await fetch(`/queue/${current.song.itemId}`, { method: "DELETE", headers: { "X-Device-Id": deviceId() } });
  } catch {}
  htmx.trigger(document.body, "queueChanged");
  poll();
}

function showIdle() {
  idle = true;
  loadToken++;
  current?.mixer?.destroy();
  current = null;
  if (ytReady) yt.stopVideo();
  showCover(false);
  $("np-title").textContent = "Nothing playing";
  $("np-author").textContent = "";
  $("mixer").hidden = true;
  setControlsEnabled(false);
  $("skip").disabled = true;
  updateTime(0, 0);
  showMessage("The queue is empty. Songs added from the catalog start here automatically.");
}

// stopPlaying silences this device after it lost the player.
function stopPlaying() {
  idle = false;
  loadToken++;
  current?.mixer?.destroy();
  current = null;
  if (ytReady) yt.stopVideo();
  hideMessage();
  showRequest(null);
  setControlsEnabled(false);
  updateTime(0, 0);
  htmx.trigger(document.body, "queueChanged"); // the skip button on the list depends on it
}

// ---------------------------------------------------------------- watching

// showWatching shows a device that isn't playing what the owner is doing, and
// what its button would do.
function showWatching(state) {
  const song = state.song;
  $("np-title").textContent = song ? song.title || "Untitled" : "Nothing playing";
  $("np-author").textContent = song?.author || "";
  if (song) {
    $("cover").src = `https://i.ytimg.com/vi/${song.videoId}/hqdefault.jpg`;
  }
  showCover(Boolean(song));
  hideMessage();

  const button = $("start-button");
  const owner = state.ownerName || "another device";
  let label, text;
  if (state.playing && state.requested) {
    label = `Waiting for ${owner}…`;
    text = "You asked to play the queue here. It starts as soon as they let you.";
  } else if (state.playing) {
    label = "Ask to play here";
    text = `The queue is playing on ${owner}'s device${statusText(state)}. You can still add and remove songs.`;
  } else if (state.owner) {
    label = "▶ Resume player";
    text = "This screen was playing the queue. Browsers need a click to play sound again.";
  } else if (audioCtx) {
    label = "▶ Play here";
    text = "Nobody is playing the queue.";
  } else {
    label = "▶ Start player";
    text = "Browsers only play sound after a click.\nAfter that, songs in the queue play one after another.";
  }
  button.textContent = label;
  button.disabled = state.playing && state.requested;
  $("start-text").textContent = text;
  $("start-overlay").hidden = false;
}

function statusText(state) {
  const st = state.status;
  if (!state.song || st.itemId !== state.song.itemId) return "";
  return st.paused ? `, paused at ${formatTime(st.position)}` : `, at ${formatTime(st.position)}`;
}

// showRequest shows the owner who is asking to take over, if anyone.
function showRequest(requester) {
  $("request-banner").hidden = !requester;
  if (requester) $("request-text").textContent = `${requester} wants to play the queue on their device.`;
}

async function post(url) {
  try {
    return await fetch(url, { method: "POST", headers: { "X-Device-Id": deviceId() } });
  } catch {
    return null;
  }
}

// ---------------------------------------------------------------- UI

function updateTime(time, duration) {
  $("time-current").textContent = formatTime(time);
  $("time-total").textContent = formatTime(duration);
  if (!seeking) $("seek").value = time;
}

function updatePlayButton() {
  const playing = Boolean(current?.mixer?.playing);
  $("play-pause").textContent = playing ? "⏸" : "▶";
  $("play-pause").setAttribute("aria-label", playing ? "Pause" : "Play");
}

function setControlsEnabled(enabled) {
  $("play-pause").disabled = !enabled;
  $("seek").disabled = !enabled;
  updatePlayButton();
}

function showMessage(text) {
  $("screen-message").textContent = text;
  $("screen-message").hidden = false;
}

function hideMessage() {
  $("screen-message").hidden = true;
}

function tick() {
  const mixer = current?.mixer;
  if (!mixer) return;
  const time = mixer.time();
  updateTime(time, mixer.duration);
  if (mixer.playing && time >= mixer.duration - 0.05) {
    finish();
    return;
  }
  syncVideo(time, mixer.playing);
}

$("start-button").addEventListener("click", async () => {
  // the click is what lets the page make sound, even if it only gets to play
  // later, once the owner lets it
  audioCtx ??= new AudioContext();
  await audioCtx.resume();
  if (lastState?.playing && !lastState.owner) {
    const res = await post("/player/request");
    if (res?.status === 409) claimNext = true; // nobody is playing anymore: just play
  } else {
    claimNext = true;
  }
  poll();
});

$("request-accept").addEventListener("click", async () => {
  showRequest(null);
  await post("/player/request/accept");
  poll(); // the new owner is in charge now: this one goes quiet
});

$("request-deny").addEventListener("click", () => {
  showRequest(null);
  post("/player/request/deny");
});

$("release").addEventListener("click", async () => {
  // stop renewing first, or the next poll would take the free player right back
  isOwner = false;
  stopPlaying();
  await post("/player/release");
  poll();
});

$("play-pause").addEventListener("click", () => {
  const mixer = current?.mixer;
  if (!mixer) return;
  if (mixer.playing) mixer.pause();
  else mixer.play();
  updatePlayButton();
});

$("seek").addEventListener("input", () => {
  seeking = true;
  $("time-current").textContent = formatTime(Number($("seek").value));
});

$("seek").addEventListener("change", () => {
  seeking = false;
  const mixer = current?.mixer;
  if (!mixer) return;
  mixer.seek(Number($("seek").value));
  lastVideoSeek = 0; // re-sync the video right away
  updatePlayButton();
});

$("skip").addEventListener("click", finish);

loadYouTubeAPI();
poll();
setInterval(poll, POLL_MS);
setInterval(tick, TICK_MS);
