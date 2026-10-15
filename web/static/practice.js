// Practice: plays one song on this device, outside the shared queue.
//
// The audio runs in practice-worklet.js, which plays every track in lockstep
// and is the clock: it reports its position, and the page interpolates between
// reports to draw the playhead and keep the video in sync. Each track comes out
// of its own output into a GainNode here, for volume and mute.

const TICK_MS = 100;
const PREFS_KEY = "minusone.practice";

const song = JSON.parse($("song-data").textContent);

let audioCtx = null;
let engine = null;
let seeking = false;

// ---------------------------------------------------------------- engine

class Engine {
  constructor(ctx, node, tracks, duration) {
    this.ctx = ctx;
    this.node = node;
    this.tracks = tracks; // [{ type, label, muted, gain }]
    this.duration = duration;
    this.position = 0; // at reportedAt
    this.reportedAt = 0;
    this.playing = false;
    this.seq = 0;
    this.onEnded = () => {};
    node.port.onmessage = (e) => this.receive(e.data);
  }

  // load fetches and decodes every track, then hands the samples to the worklet.
  static async load(ctx, tracks, onProgress) {
    await ctx.audioWorklet.addModule("/static/practice-worklet.js");

    let loaded = 0;
    const buffers = await Promise.all(
      tracks.map(async (t) => {
        const res = await fetch(t.url);
        if (!res.ok) throw new Error(`${t.label}: HTTP ${res.status}`);
        const buffer = await ctx.decodeAudioData(await res.arrayBuffer());
        onProgress(++loaded, tracks.length);
        return buffer;
      }),
    );

    const node = new AudioWorkletNode(ctx, "practice-engine", {
      numberOfInputs: 0,
      numberOfOutputs: tracks.length,
      outputChannelCount: tracks.map(() => 2),
    });
    const withGains = tracks.map((t, i) => {
      const gain = ctx.createGain();
      node.connect(gain, i);
      gain.connect(ctx.destination);
      return { ...t, gain };
    });
    const engine = new Engine(ctx, node, withGains, Math.max(...buffers.map((b) => b.duration)));

    // copies, so the worklet owns them and the decoded buffers can be freed
    const samples = buffers.map((b) => ({
      left: b.getChannelData(0).slice(),
      right: b.numberOfChannels > 1 ? b.getChannelData(1).slice() : null,
    }));
    const transfer = samples.flatMap((s) => (s.right ? [s.left.buffer, s.right.buffer] : [s.left.buffer]));
    engine.send({ type: "load", tracks: samples }, transfer);
    return engine;
  }

  send(message, transfer = []) {
    this.node.port.postMessage({ ...message, seq: ++this.seq }, transfer);
  }

  receive(m) {
    if (m.type === "ended") {
      this.onEnded();
      return;
    }
    if (m.seq !== this.seq) return; // from before the last command
    this.position = m.position;
    this.reportedAt = m.time;
    this.playing = m.playing;
  }

  time() {
    if (!this.playing) return this.position;
    return Math.min(this.position + Math.max(this.ctx.currentTime - this.reportedAt, 0), this.duration);
  }

  // The commands update the local state right away; the next report confirms it.
  play() {
    if (this.position >= this.duration) this.position = 0;
    this.reportedAt = this.ctx.currentTime;
    this.playing = true;
    this.send({ type: "play" });
  }

  pause() {
    this.position = this.time();
    this.playing = false;
    this.send({ type: "pause" });
  }

  seek(seconds) {
    this.position = Math.min(Math.max(seconds, 0), this.duration);
    this.reportedAt = this.ctx.currentTime;
    this.send({ type: "seek", position: this.position });
  }

  setGain(track, value, immediate = false) {
    if (immediate) track.gain.gain.value = value;
    else track.gain.gain.setTargetAtTime(value, this.ctx.currentTime, 0.02);
  }
}

// ---------------------------------------------------------------- page

async function start() {
  cueVideo(song.videoId);
  if (song.tracks.length === 0) {
    showMessage("This song has no audio.");
    return;
  }
  if (!window.isSecureContext || !window.AudioWorkletNode) {
    showMessage("Practice needs the app to be opened over HTTPS (or on localhost).");
    return;
  }

  // Created before any click: it starts suspended, which is enough to decode.
  // The play button resumes it.
  audioCtx = new AudioContext();
  showMessage("Loading audio…");
  try {
    engine = await Engine.load(audioCtx, song.tracks, (done, total) => {
      if (total > 1) showMessage(`Loading audio ${done}/${total}…`);
    });
  } catch (err) {
    showMessage(`Couldn't load the audio (${err.message}).`);
    return;
  }
  engine.onEnded = updatePlayButton;

  buildMixer(song, engine, PREFS_KEY);
  hideMessage();
  $("seek").max = engine.duration;
  setControlsEnabled(true);
  updateTime();
  setInterval(tick, TICK_MS);
}

function tick() {
  updateTime();
  updatePlayButton();
  syncVideo(engine.time(), engine.playing);
}

function updateTime() {
  const time = engine.time();
  $("time-current").textContent = formatTime(time);
  $("time-total").textContent = formatTime(engine.duration);
  if (!seeking) $("seek").value = time;
}

function updatePlayButton() {
  const playing = Boolean(engine?.playing);
  $("play-pause").textContent = playing ? "⏸" : "▶";
  $("play-pause").setAttribute("aria-label", playing ? "Pause" : "Play");
}

function setControlsEnabled(enabled) {
  for (const id of ["play-pause", "seek"]) $(id).disabled = !enabled;
  updatePlayButton();
}

function showMessage(text) {
  $("screen-message").textContent = text;
  $("screen-message").hidden = false;
}

function hideMessage() {
  $("screen-message").hidden = true;
}

async function togglePlay() {
  if (!engine) return;
  await audioCtx.resume();
  if (engine.playing) engine.pause();
  else engine.play();
  updatePlayButton();
}

function seekTo(seconds) {
  if (!engine) return;
  engine.seek(seconds);
  lastVideoSeek = 0; // re-sync the video right away
  updateTime();
}

$("play-pause").addEventListener("click", togglePlay);

$("seek").addEventListener("input", () => {
  seeking = true;
  $("time-current").textContent = formatTime(Number($("seek").value));
});

$("seek").addEventListener("change", () => {
  seeking = false;
  seekTo(Number($("seek").value));
});

loadYouTubeAPI();
start();
