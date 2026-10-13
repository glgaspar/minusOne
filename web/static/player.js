// Player: plays the song at the head of the shared queue.
//
// Audio: every track is downloaded, decoded into an AudioBuffer and played
// through the Web Audio API. All tracks are started at the same instant, so
// they stay sample-accurate with each other; each one goes through its own
// GainNode for volume and mute.
//
// Video: a muted YouTube embed. The audio is the clock; the video follows it
// and is re-seeked when it drifts.
//
// The server's queue is the source of truth: the player polls the current song,
// so a skip or a new song from any device reaches it on its own.

const POLL_MS = 3000;
const TICK_MS = 250;
const SYNC_TOLERANCE_S = 0.3; // video drift allowed before re-seeking
const SYNC_COOLDOWN_MS = 2000; // time a re-seek gets to settle before the next one
const PREFS_KEY = "minusone.mixer";

const $ = (id) => document.getElementById(id);

let audioCtx = null;
let current = null; // { song, mixer, finishing }
let loadToken = 0;
let seeking = false;
let idle = false; // the empty-queue screen is showing

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

// ---------------------------------------------------------------- video

let yt = null;
let ytReady = false;
let videoAvailable = true;
let lastVideoSeek = 0;

window.onYouTubeIframeAPIReady = () => {
  yt = new YT.Player("yt-player", {
    width: "100%",
    height: "100%",
    playerVars: {
      controls: 0,
      disablekb: 1,
      fs: 0,
      iv_load_policy: 3,
      playsinline: 1,
      rel: 0,
      mute: 1,
      origin: location.origin,
    },
    events: {
      onReady: () => {
        ytReady = true;
        yt.mute();
        if (current) cueVideo(current.song.videoId);
      },
      // 101/150: the owner doesn't allow embedding; 100: removed. The audio
      // doesn't depend on the video, so show the thumbnail and carry on.
      onError: () => {
        videoAvailable = false;
        showCover(true);
      },
    },
  });
};

function loadYouTubeAPI() {
  const script = document.createElement("script");
  script.src = "https://www.youtube.com/iframe_api";
  document.head.append(script);
}

function cueVideo(videoId) {
  videoAvailable = true;
  $("cover").src = `https://i.ytimg.com/vi/${videoId}/hqdefault.jpg`;
  showCover(false);
  if (ytReady) yt.cueVideoById(videoId);
}

function showCover(show) {
  $("cover").hidden = !show;
}

function syncVideo(audioTime, playing) {
  if (!ytReady || !videoAvailable) return;
  const state = yt.getPlayerState();
  if (!playing) {
    if (state === YT.PlayerState.PLAYING) yt.pauseVideo();
    return;
  }
  // past the video's end (or the video is shorter): let it rest
  if (state === YT.PlayerState.ENDED || audioTime >= yt.getDuration() - 0.5) return;
  if (state !== YT.PlayerState.PLAYING && state !== YT.PlayerState.BUFFERING) yt.playVideo();

  const now = performance.now();
  if (now - lastVideoSeek > SYNC_COOLDOWN_MS && Math.abs(yt.getCurrentTime() - audioTime) > SYNC_TOLERANCE_S) {
    yt.seekTo(audioTime, true);
    lastVideoSeek = now;
  }
}

// ---------------------------------------------------------------- mixer prefs

// The original's preference depends on whether the song has split tracks:
// alone it's the only sound, next to the stems it would double them.
function prefKey(track, hasStems) {
  if (track.type !== "original") return track.type;
  return hasStems ? "original:with-tracks" : "original:alone";
}

function loadPrefs() {
  try {
    return JSON.parse(localStorage.getItem(PREFS_KEY)) || {};
  } catch {
    return {};
  }
}

function savePref(key, pref) {
  try {
    const prefs = loadPrefs();
    prefs[key] = pref;
    localStorage.setItem(PREFS_KEY, JSON.stringify(prefs));
  } catch {}
}

function buildMixer(song, mixer) {
  const box = $("mixer");
  box.replaceChildren();
  const prefs = loadPrefs();
  const hasStems = song.tracks.some((t) => t.type !== "original");

  for (const track of mixer.tracks) {
    const key = prefKey(track, hasStems);
    const pref = { volume: 1, muted: track.muted, ...prefs[key] };

    const row = $("track-template").content.firstElementChild.cloneNode(true);
    const muteButton = row.querySelector(".track-mute");
    const volume = row.querySelector(".track-volume");
    row.querySelector(".track-label").textContent = track.label;
    volume.value = pref.volume;
    volume.setAttribute("aria-label", `${track.label} volume`);

    const apply = (immediate) => {
      mixer.setGain(track, pref.muted ? 0 : pref.volume, immediate);
      muteButton.setAttribute("aria-pressed", String(pref.muted));
      muteButton.textContent = pref.muted ? "Unmute" : "Mute";
      row.classList.toggle("is-muted", pref.muted);
    };
    muteButton.addEventListener("click", () => {
      pref.muted = !pref.muted;
      apply();
      savePref(key, pref);
    });
    volume.addEventListener("input", () => {
      pref.volume = Number(volume.value);
      apply();
      savePref(key, pref);
    });

    apply(true);
    box.append(row);
  }
  box.hidden = false;
}

// ---------------------------------------------------------------- queue

async function poll() {
  let res;
  try {
    res = await fetch("/player/current", { headers: { Accept: "application/json" } });
  } catch {
    return; // offline for a moment: keep playing what we have
  }
  if (res.redirected) {
    location.href = "/login"; // session expired
    return;
  }
  if (res.status === 204) {
    if (!idle) showIdle();
    return;
  }
  if (!res.ok) return;

  const song = await res.json();
  if (!current || current.song.itemId !== song.itemId) loadSong(song);
}

async function loadSong(song) {
  const token = ++loadToken;
  idle = false;
  current?.mixer?.destroy();
  current = { song, mixer: null, finishing: false };

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
  buildMixer(song, mixer);
  hideMessage();
  setControlsEnabled(true);
  $("seek").max = mixer.duration;
  mixer.play();
  updatePlayButton();
}

// Removes the current song from the queue; the next poll loads the next one.
async function finish() {
  if (!current || current.finishing) return;
  current.finishing = true;
  current.mixer?.pause();
  updatePlayButton();
  try {
    await fetch(`/queue/${current.song.itemId}`, { method: "DELETE" });
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

// ---------------------------------------------------------------- UI

function formatTime(seconds) {
  const s = Math.floor(seconds);
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

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
  audioCtx = new AudioContext();
  await audioCtx.resume();
  $("start-overlay").hidden = true;
  poll();
  setInterval(poll, POLL_MS);
  setInterval(tick, TICK_MS);
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
