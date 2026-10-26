// Practice: plays one song on this device, outside the shared queue.
//
// The audio runs in practice-worklet.js, which plays every track in lockstep
// and is the clock: it reports its position, and the page interpolates between
// reports to draw the playhead and keep the video in sync. Each track comes out
// of its own output into a GainNode here, for volume and mute.
//
// Learning tools: an A–B loop picked on the song's waveform, a count-in
// before playing and before every repeat, mixer presets and solo, and slowing
// down or changing the key (the worklet does that with SoundTouch). Loops can
// be saved as named sections, which are kept on the server per user. And you
// can record yourself over the song: the take stays on this page (and can be
// downloaded), it's never uploaded. They are saved per song on this
// device. Most things have a keyboard shortcut (the "?" dialog lists them).

const SYNC_MS = 250;
const PREFS_KEY = "minusone.practice";
const PEAK_BUCKETS = 2000;
const MIN_LOOP_S = 0.5;
const NUDGE_S = 5;
const SPEEDS = { min: 50, max: 120, step: 5 }; // percent
const KEYS = { min: -6, max: 6 }; // semitones
const FINE_NUDGE_S = 1;

// Mixer presets: which backing plays (the first of these the song has, see
// backingOf) and whether the vocals do.
const PRESETS = {
  full: { backing: ["stems", "instrumental", "original"], vocals: true },
  karaoke: { backing: ["instrumental", "stems", "original"], vocals: false },
  vocals: { backing: [], vocals: true },
};

// presetMuted says whether a preset mutes a track type.
function presetMuted(name, type) {
  const preset = PRESETS[name];
  const has = new Set(song.tracks.map((t) => backingOf(t.type)));
  const backing = preset.backing.find((b) => has.has(b));
  return backingOf(type) === "vocals" ? !preset.vocals : backingOf(type) !== backing;
}

const song = JSON.parse($("song-data").textContent);

let audioCtx = null;
let engine = null;
let seeking = false;
let settings = loadSettings();
let mixer = null; // buildMixer's controls
let markers = []; // [{ id, label, start, end }], in song order
let recording = null; // { stream, source } while recording
let take = null; // { samples, start, tempo, semitones, url } of the last recording

// ---------------------------------------------------------------- engine

class Engine {
  constructor(ctx, node, tracks, duration, peaks) {
    this.ctx = ctx;
    this.node = node;
    this.tracks = tracks; // [{ type, label, muted, gain }]
    this.takeTrack = null; // the take's mixer track, set by load
    this.duration = duration;
    this.peaks = peaks; // { min, max } Float32Arrays of PEAK_BUCKETS
    this.position = 0; // at reportedAt
    this.reportedAt = 0;
    this.counting = 0; // seconds of count-in left at reportedAt
    this.playing = false;
    this.loop = null; // { a, b } while looping, in seconds
    this.countIn = 0;
    this.tempo = 1; // song seconds per second
    this.semitones = 0;
    this.seq = 0;
    this.onEnded = () => {};
    this.onTake = () => {};
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

    // one output per track, one for the count-in clicks and one for the
    // recorded take; the input is the microphone, while recording
    const node = new AudioWorkletNode(ctx, "practice-engine", {
      numberOfInputs: 1,
      numberOfOutputs: tracks.length + 2,
      outputChannelCount: [...tracks.map(() => 2), 2, 2],
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
    const takeGain = ctx.createGain();
    node.connect(takeGain, tracks.length + 1);
    takeGain.connect(ctx.destination);
    engine.takeTrack = { type: "take", label: "My take", muted: false, gain: takeGain };

    // copies, so the worklet owns them and the decoded buffers can be freed
    const samples = buffers.map((b) => ({
      left: b.getChannelData(0).slice(),
      right: b.numberOfChannels > 1 ? b.getChannelData(1).slice() : null,
    }));
    const transfer = samples.flatMap((s) => (s.right ? [s.left.buffer, s.right.buffer] : [s.left.buffer]));
    // the worklet's stretch follows the whole mix, so every track is cut the same way
    engine.send({ type: "load", tracks: samples, guide: Math.max(original, 0) }, transfer);
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
    if (m.type === "take") {
      this.onTake(m.take);
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
    const elapsed = Math.max(this.ctx.currentTime - this.reportedAt - this.counting, 0) * this.tempo;
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

  // setStretch changes the speed (1 is normal) and the key, in semitones.
  setStretch(tempo, semitones) {
    this.position = this.time();
    this.reportedAt = this.ctx.currentTime;
    this.counting = 0;
    this.tempo = tempo;
    this.semitones = semitones;
    this.send({ type: "stretch", tempo, semitones });
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
  const defaults = { loop: null, looping: false, countIn: 0, speed: 100, key: 0 };
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
  $("save-loop").disabled = !loop;
  renderMarkers();
  drawWaveform();
}

function setCountIn(seconds) {
  settings.countIn = seconds;
  saveSettings();
  engine.setCountIn(seconds);
  $("count-in").value = String(seconds);
}

// ---------------------------------------------------------------- speed and key

// setStretch changes the speed (percent) and key (semitones), within limits.
function setStretch(speed, key) {
  settings.speed = Math.min(Math.max(speed, SPEEDS.min), SPEEDS.max);
  settings.key = Math.min(Math.max(key, KEYS.min), KEYS.max);
  saveSettings();
  if (engine.tempo !== settings.speed / 100 || engine.semitones !== settings.key) {
    engine.setStretch(settings.speed / 100, settings.key);
    lastVideoSeek = 0;
  }
  $("speed").value = settings.speed;
  $("key").value = settings.key;
  updateStretchLabels();
  updateTakeInfo();
}

function updateStretchLabels() {
  const speed = Number($("speed").value);
  const key = Number($("key").value);
  $("speed-value").textContent = `${speed}%`;
  $("key-value").textContent = key > 0 ? `+${key}` : String(key);
  $("speed-reset").hidden = speed === 100;
  $("key-reset").hidden = key === 0;
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

  // saved sections: a bar along the top with the name, if it fits
  g.font = `${11 * dpr}px system-ui, sans-serif`;
  g.textBaseline = "top";
  for (const m of markers) {
    const from = x(m.start);
    const w = x(m.end) - from;
    g.fillStyle = color("--muted");
    g.fillRect(from, 0, w, 3 * dpr);
    if (g.measureText(m.label).width + 4 * dpr < w) {
      g.fillStyle = color("--text");
      g.fillText(m.label, from + 2 * dpr, 5 * dpr);
    }
  }
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

// ---------------------------------------------------------------- markers

const markersURL = `/songs/${song.songId}/markers`;

async function loadMarkers() {
  try {
    const res = await fetch(markersURL, { headers: { Accept: "application/json" } });
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    markers = await res.json();
  } catch {
    toast("Couldn't load your saved sections.", true);
  }
  renderMarkers();
}

async function saveMarker(label) {
  const { a, b } = settings.loop;
  const res = await fetch(markersURL, {
    method: "POST",
    headers: { "Content-Type": "application/json", Accept: "application/json" },
    body: JSON.stringify({ label, start: a, end: b }),
  }).catch(() => null);
  if (!res?.ok) {
    const message = await res?.json().then((e) => e.message).catch(() => null);
    toast(message ? `Couldn't save it: ${message}.` : "Couldn't save the section.", true);
    return false;
  }
  markers.push(await res.json());
  markers.sort((x, y) => x.start - y.start || x.end - y.end);
  renderMarkers();
  return true;
}

async function deleteMarker(marker) {
  const res = await fetch(`${markersURL}/${marker.id}`, { method: "DELETE" }).catch(() => null);
  if (!res?.ok && res?.status !== 404) {
    toast("Couldn't delete the section.", true);
    return;
  }
  markers = markers.filter((m) => m.id !== marker.id);
  renderMarkers();
}

function useMarker(marker) {
  setLoop({ a: marker.start, b: marker.end }, true);
  seekTo(marker.start);
}

function renderMarkers() {
  const list = $("markers");
  list.replaceChildren();
  const loop = settings.loop;
  for (const m of markers) {
    const li = document.createElement("li");
    li.className = "marker";
    const active = loop && Math.abs(loop.a - m.start) < 0.01 && Math.abs(loop.b - m.end) < 0.01;
    li.classList.toggle("is-active", Boolean(active));

    const chip = document.createElement("button");
    chip.type = "button";
    chip.className = "marker-chip";
    chip.textContent = m.label;
    chip.title = `Loop ${formatTime(m.start, true)} – ${formatTime(m.end, true)}`;
    chip.addEventListener("click", () => useMarker(m));

    const remove = document.createElement("button");
    remove.type = "button";
    remove.className = "icon-link icon-delete";
    remove.textContent = "×";
    remove.setAttribute("aria-label", `Delete ${m.label}`);
    remove.addEventListener("click", () => deleteMarker(m));

    li.append(chip, remove);
    list.append(li);
  }
  drawWaveform();
}

function showMarkerForm(show) {
  $("marker-form").hidden = !show;
  $("save-loop").hidden = show;
  if (show) {
    $("marker-label").value = "";
    $("marker-label").focus();
  }
}

$("save-loop").addEventListener("click", () => showMarkerForm(true));
$("marker-cancel").addEventListener("click", () => showMarkerForm(false));
$("marker-label").addEventListener("keydown", (e) => {
  if (e.key === "Escape") showMarkerForm(false);
});
$("marker-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const label = $("marker-label").value.trim();
  if (!label || !settings.loop) return;
  if (await saveMarker(label)) showMarkerForm(false);
});

// ---------------------------------------------------------------- recording

// showMixer builds the mixer, with a row for the take once there is one.
function showMixer() {
  const tracks = take ? [...engine.tracks, engine.takeTrack] : engine.tracks;
  mixer = buildMixer(song, { tracks, setGain: (t, v, i) => engine.setGain(t, v, i) }, PREFS_KEY, {
    solo: true,
    onChange: updatePresets,
  });
}

async function toggleRecording() {
  if (!engine) return;
  if (recording) {
    engine.send({ type: "record", on: false }); // the take comes back in receiveTake
    return;
  }
  let stream;
  try {
    // the raw voice: the browser's processing smears it and adds delay
    stream = await navigator.mediaDevices.getUserMedia({
      audio: { echoCancellation: false, noiseSuppression: false, autoGainControl: false },
    });
  } catch {
    toast("Couldn't use the microphone. Check that the browser may use it.", true);
    return;
  }
  await audioCtx.resume();
  const source = audioCtx.createMediaStreamSource(stream);
  source.connect(engine.node);
  recording = { stream, source };
  dropTake();

  // what you hear left the speakers a while ago, and your voice takes a while to get here
  const inputLatency = stream.getAudioTracks()[0]?.getSettings().latency ?? 0;
  const latency = (audioCtx.baseLatency || 0) + (audioCtx.outputLatency || 0) + inputLatency;
  if (!engine.playing) engine.play();
  engine.send({ type: "record", on: true, latency });
  updateRecordUI();
}

function receiveTake(received) {
  if (recording) {
    recording.source.disconnect();
    for (const track of recording.stream.getTracks()) track.stop();
    recording = null;
  }
  if (received) {
    take = { ...received, url: URL.createObjectURL(wavBlob(received.samples, audioCtx.sampleRate)) };
    $("take-download").href = take.url;
    $("take-offset").value = 0;
    updateTakeOffset();
    engine.send({ type: "takeOffset", seconds: 0 });
    showMixer();
    updatePresets();
  }
  updateRecordUI();
}

function dropTake() {
  if (!take) return;
  URL.revokeObjectURL(take.url);
  take = null;
  showMixer();
  updatePresets();
}

function updateRecordUI() {
  $("record").textContent = recording ? "■ Stop" : "● Record";
  $("record").setAttribute("aria-pressed", String(Boolean(recording)));
  for (const id of ["take-offset-field", "take-offset", "take-download"]) $(id).hidden = !take || Boolean(recording);
  updateTakeInfo();
}

function updateTakeInfo() {
  const info = $("take-info");
  if (recording) {
    info.textContent = engine.loop ? "Recording until the end of the loop…" : "Recording…";
  } else if (!take) {
    info.textContent = "Use headphones, or the microphone records the song too.";
  } else if (take.tempo !== engine.tempo || take.semitones !== engine.semitones) {
    const key = take.semitones > 0 ? `+${take.semitones}` : String(take.semitones);
    info.textContent = `Recorded at ${Math.round(take.tempo * 100)}% speed and key ${key}: set them back to hear it.`;
  } else {
    info.textContent = `Your take, ${formatTime(take.samples.length / audioCtx.sampleRate, true)} long, plays along with the song.`;
  }
}

function updateTakeOffset() {
  const ms = Number($("take-offset").value);
  $("take-offset-value").textContent = `${ms > 0 ? "+" : ""}${ms} ms`;
}

// wavBlob encodes mono samples as a 16-bit WAV file.
function wavBlob(samples, rate) {
  const view = new DataView(new ArrayBuffer(44 + samples.length * 2));
  const text = (at, s) => [...s].forEach((c, i) => view.setUint8(at + i, c.charCodeAt(0)));
  text(0, "RIFF");
  view.setUint32(4, 36 + samples.length * 2, true);
  text(8, "WAVEfmt ");
  view.setUint32(16, 16, true);
  view.setUint16(20, 1, true); // PCM
  view.setUint16(22, 1, true); // mono
  view.setUint32(24, rate, true);
  view.setUint32(28, rate * 2, true);
  view.setUint16(32, 2, true);
  view.setUint16(34, 16, true);
  text(36, "data");
  view.setUint32(40, samples.length * 2, true);
  samples.forEach((v, i) => view.setInt16(44 + i * 2, Math.max(-1, Math.min(1, v)) * 0x7fff, true));
  return new Blob([view], { type: "audio/wav" });
}

$("record").addEventListener("click", toggleRecording);
$("take-offset").addEventListener("input", updateTakeOffset);
$("take-offset").addEventListener("change", () => {
  engine.send({ type: "takeOffset", seconds: Number($("take-offset").value) / 1000 });
});

// ---------------------------------------------------------------- presets

function setupPresets() {
  const ways = song.tracks.map((t) => backingOf(t.type));
  const usable = ways.includes("vocals") && (ways.includes("instrumental") || ways.includes("stems"));
  for (const button of document.querySelectorAll("[data-preset]")) button.disabled = !usable;
  $("presets-hint").hidden = usable;
  $("presets").hidden = false;
  updatePresets();
}

function applyPreset(name) {
  for (const row of mixer.rows) {
    if (row.type !== "take") mixer.setMuted(row.type, presetMuted(name, row.type));
  }
  mixer.clearSolo();
  mixer.commit();
}

// updatePresets highlights the preset the mixer matches, if any.
function updatePresets() {
  if (!mixer) return;
  const soloing = mixer.rows.some((r) => r.soloed);
  for (const button of document.querySelectorAll("[data-preset]")) {
    const name = button.dataset.preset;
    const active = !button.disabled && !soloing && mixer.rows.every((r) => r.type === "take" || r.pref.muted === presetMuted(name, r.type));
    button.setAttribute("aria-pressed", String(active));
  }
}

for (const button of document.querySelectorAll("[data-preset]")) {
  button.addEventListener("click", () => applyPreset(button.dataset.preset));
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

  engine.onTake = receiveTake;
  showMixer();
  setupPresets();
  hideMessage();
  $("seek").max = engine.duration;
  $("tools").hidden = false;
  setControlsEnabled(true);
  setCountIn(settings.countIn);
  setLoop(settings.loop);
  setStretch(settings.speed, settings.key);
  loadMarkers();
  requestAnimationFrame(frame);
  setInterval(() => syncVideo(engine.time(), engine.playing && !engine.isCounting(), engine.tempo), SYNC_MS);
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
  for (const id of ["play-pause", "seek", "back-5", "forward-5", "record"]) $(id).disabled = !enabled;
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
// the labels follow the sliders; the audio changes when they're let go
$("speed").addEventListener("input", updateStretchLabels);
$("key").addEventListener("input", updateStretchLabels);
$("speed").addEventListener("change", () => setStretch(Number($("speed").value), settings.key));
$("key").addEventListener("change", () => setStretch(settings.speed, Number($("key").value)));
$("speed-reset").addEventListener("click", () => setStretch(100, settings.key));
$("key-reset").addEventListener("click", () => setStretch(settings.speed, 0));

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
  "-": () => setStretch(settings.speed - SPEEDS.step, settings.key),
  "=": () => setStretch(settings.speed + SPEEDS.step, settings.key),
  "+": () => setStretch(settings.speed + SPEEDS.step, settings.key),
  ",": () => setStretch(settings.speed, settings.key - 1),
  ".": () => setStretch(settings.speed, settings.key + 1),
  r: toggleRecording,
  R: toggleRecording,
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
