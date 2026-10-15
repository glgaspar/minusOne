// Practice: plays one song on this device, outside the shared queue.
//
// The audio runs in practice-worklet.js, which plays every track in lockstep
// and is the clock: it reports its position, and the page interpolates between
// reports to draw the playhead and keep the video in sync. Each track comes out
// of its own output into a GainNode here, for volume and mute.
//
// Learning tools: an A–B loop picked on the song's waveform, and a count-in
// before playing and before every repeat. They are saved per song on this
// device. Most things have a keyboard shortcut (the "?" dialog lists them).

const SYNC_MS = 250;
const PREFS_KEY = "minusone.practice";
const PEAK_BUCKETS = 2000;
const MIN_LOOP_S = 0.5;
const NUDGE_S = 5;
const FINE_NUDGE_S = 1;

const song = JSON.parse($("song-data").textContent);

let audioCtx = null;
let engine = null;
let seeking = false;
let settings = loadSettings();

// ---------------------------------------------------------------- engine

class Engine {
  constructor(ctx, node, tracks, duration, peaks) {
    this.ctx = ctx;
    this.node = node;
    this.tracks = tracks; // [{ type, label, muted, gain }]
    this.duration = duration;
    this.peaks = peaks; // { min, max } Float32Arrays of PEAK_BUCKETS
    this.position = 0; // at reportedAt
    this.reportedAt = 0;
    this.counting = 0; // seconds of count-in left at reportedAt
    this.playing = false;
    this.loop = null; // { a, b } while looping, in seconds
    this.countIn = 0;
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

    // one output per track, and one for the count-in clicks
    const node = new AudioWorkletNode(ctx, "practice-engine", {
      numberOfInputs: 0,
      numberOfOutputs: tracks.length + 1,
      outputChannelCount: [...tracks.map(() => 2), 2],
    });
    const withGains = tracks.map((t, i) => {
      const gain = ctx.createGain();
      node.connect(gain, i);
      gain.connect(ctx.destination);
      return { ...t, gain };
    });
    node.connect(ctx.destination, tracks.length);

    // the original is the whole mix; without one, the stems add up to it
    const original = tracks.findIndex((t) => t.type === "original");
    const peaks = computePeaks(original >= 0 ? [buffers[original]] : buffers);
    const engine = new Engine(ctx, node, withGains, Math.max(...buffers.map((b) => b.duration)), peaks);

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
    this.counting = m.counting;
  }

  // isCounting reports whether the count-in is playing now.
  isCounting() {
    return this.playing && this.ctx.currentTime - this.reportedAt < this.counting;
  }

  time() {
    if (!this.playing) return this.position;
    const elapsed = Math.max(this.ctx.currentTime - this.reportedAt - this.counting, 0);
    // the worklet wraps the loop; until its report arrives, wait at the end
    const end = this.loop && this.position < this.loop.b ? this.loop.b : this.duration;
    return Math.min(this.position + elapsed, end);
  }

  // The commands update the local state right away; the next report confirms it.
  play() {
    if (this.position >= this.duration) this.position = 0;
    this.reportedAt = this.ctx.currentTime;
    this.counting = this.countIn;
    this.playing = true;
    this.send({ type: "play" });
  }

  pause() {
    this.position = this.time();
    this.playing = false;
    this.counting = 0;
    this.send({ type: "pause" });
  }

  seek(seconds) {
    this.position = Math.min(Math.max(seconds, 0), this.duration);
    this.reportedAt = this.ctx.currentTime;
    this.counting = 0;
    this.send({ type: "seek", position: this.position });
  }

  setLoop(loop) {
    this.loop = loop;
    this.send({ type: "loop", a: loop?.a ?? 0, b: loop?.b ?? 0, enabled: Boolean(loop) });
  }

  setCountIn(seconds) {
    this.countIn = seconds;
    this.send({ type: "countIn", seconds });
  }

  setGain(track, value, immediate = false) {
    if (immediate) track.gain.gain.value = value;
    else track.gain.gain.setTargetAtTime(value, this.ctx.currentTime, 0.02);
  }
}

// computePeaks reduces the audio to PEAK_BUCKETS min/max pairs for the waveform.
function computePeaks(buffers) {
  const length = Math.max(...buffers.map((b) => b.length));
  const min = new Float32Array(PEAK_BUCKETS);
  const max = new Float32Array(PEAK_BUCKETS);
  const channels = buffers.flatMap((b) => Array.from({ length: b.numberOfChannels }, (_, c) => b.getChannelData(c)));
  const scale = buffers.length / channels.length; // average the channels of each buffer, add up the buffers
  for (let k = 0; k < PEAK_BUCKETS; k++) {
    const from = Math.floor((k * length) / PEAK_BUCKETS);
    const to = Math.floor(((k + 1) * length) / PEAK_BUCKETS);
    let lo = 0;
    let hi = 0;
    for (let i = from; i < to; i++) {
      let v = 0;
      for (const ch of channels) v += ch[i] ?? 0;
      v *= scale;
      if (v < lo) lo = v;
      if (v > hi) hi = v;
    }
    min[k] = lo;
    max[k] = hi;
  }
  return { min, max };
}

// ---------------------------------------------------------------- settings

// Settings are per song, on this device: the loop and the count-in you were
// practicing with are there the next time.
function loadSettings() {
  const defaults = { loop: null, looping: false, countIn: 0 };
  try {
    return { ...defaults, ...JSON.parse(localStorage.getItem(`${PREFS_KEY}.${song.songId}`)) };
  } catch {
    return defaults;
  }
}

function saveSettings() {
  try {
    localStorage.setItem(`${PREFS_KEY}.${song.songId}`, JSON.stringify(settings));
  } catch {}
}

// ---------------------------------------------------------------- loop

// setLoop changes the loop region (null clears it) and whether it repeats.
function setLoop(loop, looping = settings.looping) {
  if (loop) {
    loop = { a: Math.max(loop.a, 0), b: Math.min(loop.b, engine.duration) };
    if (loop.b - loop.a < MIN_LOOP_S) loop = null;
  }
  settings.loop = loop;
  settings.looping = Boolean(loop) && looping;
  saveSettings();
  engine.setLoop(settings.looping ? loop : null);
  updateLoopUI();
}

function setA() {
  const t = engine.time();
  const b = settings.loop && settings.loop.b - t >= MIN_LOOP_S ? settings.loop.b : engine.duration;
  setLoop({ a: t, b }, true);
}

function setB() {
  const t = engine.time();
  const a = settings.loop && t - settings.loop.a >= MIN_LOOP_S ? settings.loop.a : 0;
  setLoop({ a, b: t }, true);
}

function toggleLoop() {
  setLoop(settings.loop ?? { a: 0, b: engine.duration }, !settings.looping);
}

function updateLoopUI() {
  const loop = settings.loop;
  $("loop-toggle").setAttribute("aria-pressed", String(settings.looping));
  $("loop-range").textContent = loop ? `${formatTime(loop.a, true)} – ${formatTime(loop.b, true)}` : "No loop";
  $("loop-clear").hidden = !loop;
  drawWaveform();
}

function setCountIn(seconds) {
  settings.countIn = seconds;
  saveSettings();
  engine.setCountIn(seconds);
  $("count-in").value = String(seconds);
}

// ---------------------------------------------------------------- waveform

const canvas = $("waveform");
let drag = null; // { x, moved } while the pointer is down

function canvasTime(e) {
  const rect = canvas.getBoundingClientRect();
  return (Math.min(Math.max(e.clientX - rect.left, 0), rect.width) / rect.width) * engine.duration;
}

function drawWaveform() {
  if (!engine) return;
  const dpr = window.devicePixelRatio || 1;
  const width = Math.round(canvas.clientWidth * dpr);
  const height = Math.round(canvas.clientHeight * dpr);
  if (canvas.width !== width || canvas.height !== height) {
    canvas.width = width;
    canvas.height = height;
  }
  const g = canvas.getContext("2d");
  const css = getComputedStyle(document.documentElement);
  const color = (name) => css.getPropertyValue(name).trim();
  const x = (seconds) => (seconds / engine.duration) * width;
  g.clearRect(0, 0, width, height);

  const loop = drag?.moved ? drag.loop : settings.loop;
  if (loop) {
    g.fillStyle = color("--accent");
    g.globalAlpha = settings.looping || drag ? 0.16 : 0.07;
    g.fillRect(x(loop.a), 0, x(loop.b) - x(loop.a), height);
    g.globalAlpha = 1;
    g.fillRect(x(loop.a), 0, dpr, height);
    g.fillRect(x(loop.b) - dpr, 0, dpr, height);
  }

  const { min, max } = engine.peaks;
  const played = x(engine.time());
  const mid = height / 2;
  for (let px = 0; px < width; px++) {
    const from = Math.floor((px / width) * PEAK_BUCKETS);
    const to = Math.max(Math.floor(((px + 1) / width) * PEAK_BUCKETS), from + 1);
    let lo = 0;
    let hi = 0;
    for (let k = from; k < to; k++) {
      if (min[k] < lo) lo = min[k];
      if (max[k] > hi) hi = max[k];
    }
    g.fillStyle = px < played ? color("--accent") : color("--muted");
    g.globalAlpha = px < played ? 1 : 0.55;
    g.fillRect(px, mid - hi * mid * 0.95, 1, Math.max((hi - lo) * mid * 0.95, 1));
  }
  g.globalAlpha = 1;
  g.fillStyle = color("--text");
  g.fillRect(played - dpr, 0, 2 * dpr, height);
}

canvas.addEventListener("pointerdown", (e) => {
  if (!engine) return;
  canvas.setPointerCapture(e.pointerId);
  drag = { x: e.clientX, from: canvasTime(e), moved: false, loop: null };
});

canvas.addEventListener("pointermove", (e) => {
  if (!drag) return;
  if (Math.abs(e.clientX - drag.x) > 4) drag.moved = true;
  if (!drag.moved) return;
  const t = canvasTime(e);
  drag.loop = { a: Math.min(drag.from, t), b: Math.max(drag.from, t) };
  drawWaveform();
});

canvas.addEventListener("pointerup", (e) => {
  if (!drag) return;
  const { moved, loop } = drag;
  drag = null;
  if (moved) {
    setLoop(loop, true);
    const t = engine.time();
    if (loop && (t < loop.a || t > loop.b)) seekTo(loop.a);
  } else {
    seekTo(canvasTime(e));
  }
});

canvas.addEventListener("pointercancel", () => {
  drag = null;
  drawWaveform();
});

new ResizeObserver(() => drawWaveform()).observe(canvas);

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
  $("tools").hidden = false;
  setControlsEnabled(true);
  setCountIn(settings.countIn);
  setLoop(settings.loop);
  requestAnimationFrame(frame);
  setInterval(() => syncVideo(engine.time(), engine.playing && !engine.isCounting()), SYNC_MS);
}

function frame() {
  updateTime();
  updatePlayButton();
  drawWaveform();
  requestAnimationFrame(frame);
}

function updateTime() {
  const time = engine.time();
  $("time-current").textContent = engine.isCounting() ? "…" : formatTime(time);
  $("time-total").textContent = formatTime(engine.duration);
  if (!seeking) $("seek").value = time;
}

function updatePlayButton() {
  const playing = Boolean(engine?.playing);
  $("play-pause").textContent = playing ? "⏸" : "▶";
  $("play-pause").setAttribute("aria-label", playing ? "Pause" : "Play");
}

function setControlsEnabled(enabled) {
  for (const id of ["play-pause", "seek", "back-5", "forward-5"]) $(id).disabled = !enabled;
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

$("back-5").addEventListener("click", () => seekTo(engine.time() - NUDGE_S));
$("forward-5").addEventListener("click", () => seekTo(engine.time() + NUDGE_S));
$("set-a").addEventListener("click", setA);
$("set-b").addEventListener("click", setB);
$("loop-toggle").addEventListener("click", toggleLoop);
$("loop-clear").addEventListener("click", () => setLoop(null));
$("count-in").addEventListener("change", () => setCountIn(Number($("count-in").value)));

// ---------------------------------------------------------------- shortcuts

const shortcuts = {
  " ": togglePlay,
  ArrowLeft: (e) => seekTo(engine.time() - (e.shiftKey ? FINE_NUDGE_S : NUDGE_S)),
  ArrowRight: (e) => seekTo(engine.time() + (e.shiftKey ? FINE_NUDGE_S : NUDGE_S)),
  "[": setA,
  "]": setB,
  l: toggleLoop,
  L: toggleLoop,
  Escape: () => setLoop(null),
  "?": () => $("shortcuts").showModal(),
};

document.addEventListener("keydown", (e) => {
  const action = shortcuts[e.key];
  if (!action || !engine || e.ctrlKey || e.metaKey || e.altKey) return;
  const target = e.target;
  // typing, sliders (arrows move them) and open dialogs keep their keys,
  // and so do buttons for Space, which presses them
  if (target.closest("input, select, textarea, [contenteditable]") || document.querySelector("dialog[open]")) return;
  if (e.key === " " && target.closest("button")) return;
  e.preventDefault();
  action(e);
});

loadYouTubeAPI();
start();
