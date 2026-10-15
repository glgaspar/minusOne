// The practice page's audio engine, running on the audio thread.
//
// It holds every track's samples and plays them all in the same process()
// call, one stereo output per track, so the tracks can't drift apart. Volume
// and mute are GainNodes on the page, after the outputs.
//
// It is the clock: it owns the position, the A–B loop and the count-in, and
// reports them to the page, which interpolates between reports.
//
// The count-in is silence with a click every second, on its own output after
// the tracks', before playing from a stop and before every repeat of the loop.
//
// Speed and key go through SoundTouch (vendor/soundtouch.js, soundtouchjs
// 0.3.0): a resampler for the key and a time-stretch for the speed, per track.
// See StretchGroup for how the tracks stay aligned.
//
// Recording: the microphone is this node's input. Each recorded sample is
// tied to the song position playing in the same block, minus the time the
// sound takes to reach the speakers and come back through the microphone, so
// the take lines up with the song. The take plays on the last output, at the
// speed and key it was recorded at.

import { AbstractFifoSamplePipe, RateTransposer, Stretch } from "./vendor/soundtouch.js";

const REPORT_EVERY_S = 0.05;
const CLICK_S = 0.03;
const FEED_FRAMES = 1024;
const RECORD_CHUNK = 48000; // frames per chunk of a recording

class PracticeEngine extends AudioWorkletProcessor {
  constructor() {
    super();
    this.tracks = []; // [{ left, right }] Float32Arrays; right is null for mono
    this.guide = null; // { left, right }: the whole mix, which the stretch follows
    this.length = 0; // in frames
    this.playing = false;
    this.loop = null; // { a, b } in frames, while looping
    this.countIn = 0; // frames of count-in
    this.counting = 0; // frames of count-in left
    this.tempo = 1;
    this.semitones = 0;

    // Without a stretch, pos is the next frame to play. With one, the output
    // since the last restart started at start, and each output frame is tempo
    // source frames, so the position is start + emitted * tempo.
    this.pos = 0;
    this.stretch = null; // a StretchGroup when speed or key are changed
    this.start = 0;
    this.emitted = 0;
    this.fed = 0; // the next source frame to feed the stretch

    this.recording = null; // { chunks, frames, start, latency } while recording
    this.take = null; // { samples, start, tempo, semitones }; start is a song frame
    this.takeOffset = 0; // seconds the take plays later than recorded

    this.seq = 0; // the page's last command, echoed so it can drop stale reports
    this.lastReport = -Infinity;
    this.port.onmessage = (e) => this.command(e.data);
  }

  command(m) {
    this.seq = m.seq ?? this.seq;
    switch (m.type) {
      case "load":
        this.tracks = m.tracks;
        this.guide = m.tracks[m.guide];
        this.length = Math.max(0, ...m.tracks.map((t) => t.left.length));
        this.restart(0);
        break;
      case "play":
        if (this.position() >= this.length) this.restart(0);
        this.playing = this.length > 0;
        this.counting = this.countIn;
        break;
      case "pause":
        this.playing = false;
        this.counting = 0;
        this.finishRecording();
        break;
      case "seek":
        this.restart(Math.min(Math.max(Math.round(m.position * sampleRate), 0), this.length));
        break;
      case "loop": {
        const a = Math.round(m.a * sampleRate);
        const b = Math.min(Math.round(m.b * sampleRate), this.length);
        this.loop = m.enabled && b > a ? { a, b } : null;
        break;
      }
      case "countIn":
        this.countIn = Math.round(m.seconds * sampleRate);
        break;
      case "stretch": {
        this.finishRecording(); // before the take's speed changes
        const pos = this.position();
        this.tempo = m.tempo;
        this.semitones = m.semitones;
        const changed = m.tempo !== 1 || m.semitones !== 0;
        this.stretch = changed ? new StretchGroup(this.tracks.length, m.tempo, m.semitones) : null;
        this.restart(pos);
        break;
      }
      case "record":
        if (m.on) {
          this.take = null;
          this.recording = { chunks: [], frames: 0, start: null, latency: m.latency };
        } else {
          this.finishRecording();
        }
        break;
      case "takeOffset":
        this.takeOffset = m.seconds;
        break;
    }
    this.report(currentTime);
  }

  position() {
    if (!this.stretch) return this.pos;
    return Math.min(this.start + Math.round(this.emitted * this.tempo), this.length);
  }

  // restart continues from frame, dropping what the stretch has buffered. A
  // recording ends there: it has to be one continuous take.
  restart(frame) {
    this.finishRecording();
    this.pos = frame;
    this.start = frame;
    this.fed = frame;
    this.emitted = 0;
    this.stretch?.clear();
  }

  report(time) {
    this.lastReport = time;
    this.port.postMessage({
      type: "state",
      seq: this.seq,
      position: this.position() / sampleRate,
      playing: this.playing,
      counting: this.counting / sampleRate,
      time,
    });
  }

  process(inputs, outputs) {
    if (!this.playing) return true; // outputs stay silent

    const frames = outputs[0][0].length;
    let o = 0; // frames of this block written so far
    while (o < frames && this.playing) {
      if (this.counting > 0) {
        const n = Math.min(frames - o, this.counting);
        this.click(outputs[this.tracks.length], o, n);
        this.counting -= n;
        o += n;
        continue;
      }

      // play up to the end of the loop, if we're inside it, or of the song
      const pos = this.position();
      const looping = this.loop && pos < this.loop.b;
      const end = looping ? this.loop.b : this.length;
      const n = this.stretch ? this.renderStretched(outputs, o, frames - o, end - pos) : this.render(outputs, o, frames - o, end - pos);
      if (this.recording) this.capture(inputs[0], o, n, pos);
      else if (this.take) this.renderTake(outputs[this.tracks.length + 1], o, n, pos);
      o += n;

      if (this.position() < end) continue;
      if (looping) {
        this.restart(this.loop.a);
        this.counting = this.countIn;
        this.lastReport = -Infinity; // report the jump right away
      } else {
        this.playing = false;
        this.finishRecording();
        this.port.postMessage({ type: "ended" });
      }
    }

    const time = currentTime + frames / sampleRate;
    if (!this.playing || time - this.lastReport >= REPORT_EVERY_S) this.report(time);
    return true;
  }

  // render copies up to want frames, at most the remaining frames, straight
  // from the tracks. It returns how many it wrote.
  render(outputs, o, want, remaining) {
    const n = Math.min(want, remaining);
    for (let t = 0; t < this.tracks.length; t++) {
      const { left, right } = this.tracks[t];
      outputs[t][0].set(left.subarray(this.pos, this.pos + n), o);
      outputs[t][1].set((right ?? left).subarray(this.pos, this.pos + n), o);
    }
    this.pos += n;
    return n;
  }

  // renderStretched is render through the stretch: it feeds it until it has
  // enough output, past the end of the song with silence.
  renderStretched(outputs, o, want, remaining) {
    const n = Math.min(want, Math.ceil(remaining / this.tempo));
    while (this.stretch.available() < n) {
      this.stretch.feed(this.tracks, this.guide, this.fed, FEED_FRAMES);
      this.fed += FEED_FRAMES;
    }
    this.stretch.receive(outputs, o, n);
    this.emitted += n;
    return n;
  }

  // capture records n input frames from offset o, which were sung over the
  // song playing from frame pos.
  capture(input, o, n, pos) {
    const rec = this.recording;
    if (rec.start === null) rec.start = pos - rec.latency * sampleRate * this.tempo;
    for (let i = 0; i < n; i++) {
      const at = rec.frames % RECORD_CHUNK;
      if (at === 0) rec.chunks.push(new Float32Array(RECORD_CHUNK));
      // the first channel; silence if the microphone isn't connected yet
      rec.chunks[rec.chunks.length - 1][at] = input[0]?.[o + i] ?? 0;
      rec.frames++;
    }
  }

  // finishRecording turns the recording into the take and sends the page a copy.
  finishRecording() {
    const rec = this.recording;
    if (!rec) return;
    this.recording = null;
    if (rec.frames === 0) {
      this.port.postMessage({ type: "take", take: null });
      return;
    }
    const samples = new Float32Array(rec.frames);
    rec.chunks.forEach((chunk, i) => samples.set(chunk.subarray(0, Math.min(RECORD_CHUNK, rec.frames - i * RECORD_CHUNK)), i * RECORD_CHUNK));
    this.take = { samples, start: rec.start, tempo: this.tempo, semitones: this.semitones };
    const copy = samples.slice();
    this.port.postMessage(
      { type: "take", take: { samples: copy, start: rec.start / sampleRate, tempo: this.tempo, semitones: this.semitones } },
      [copy.buffer],
    );
  }

  // renderTake writes the take's part of n output frames from offset o, for
  // the song playing from frame pos. A take only plays at the speed and key
  // it was recorded at: it is the singer's own timing and pitch.
  renderTake(out, o, n, pos) {
    const take = this.take;
    if (this.tempo !== take.tempo || this.semitones !== take.semitones) return;
    const first = (pos - take.start) / this.tempo - this.takeOffset * sampleRate;
    for (let i = 0; i < n; i++) {
      const k = Math.round(first + i);
      if (k < 0 || k >= take.samples.length) continue;
      out[0][o + i] = take.samples[k];
      out[1][o + i] = take.samples[k];
    }
  }

  // click writes n frames of the count-in from offset o: a short beep at the
  // start of every second.
  click(out, o, n) {
    const done = this.countIn - this.counting; // frames of count-in already played
    for (let i = 0; i < n; i++) {
      const t = ((done + i) % sampleRate) / sampleRate;
      if (t >= CLICK_S) continue;
      const v = 0.4 * Math.sin(2 * Math.PI * 1500 * t) * (1 - t / CLICK_S);
      out[0][o + i] = v;
      out[1][o + i] = v;
    }
  }
}

// StretchGroup changes the speed and key of every track the same way.
//
// SoundTouch's time-stretch cuts the audio into pieces and overlaps each one
// where it best matches the previous, which depends on the audio. Stretching
// each track on its own would pick different cuts per track and smear them
// against each other. So one more chain, the guide, gets the whole mix and
// picks the cuts, and every track makes the same ones (linkedStretch).
//
// Each chain mirrors SoundTouch's: the resampler moves the key, the stretch
// fixes the length, and the resampler goes first unless it's raising the pitch.
class StretchGroup {
  constructor(tracks, tempo, semitones) {
    const pitch = 2 ** (semitones / 12);
    this.rate = pitch;
    this.stretchTempo = tempo / pitch;
    this.chains = Array.from({ length: tracks + 1 }, () => this.chain()); // the last is the guide
    this.scratch = this.chains.map(() => new Float32Array(FEED_FRAMES * 2));
  }

  chain() {
    // AbstractFifoSamplePipe(true) is the way to get the library's FIFOs
    const ends = new AbstractFifoSamplePipe(true); // its input and output buffers
    const middle = new AbstractFifoSamplePipe(true).inputBuffer;
    const transposer = new RateTransposer(false);
    const stretch = new Stretch(false);
    stretch.setParameters(sampleRate, 0, 0, 8); // its default assumes 44.1 kHz
    stretch.tempo = this.stretchTempo;
    transposer.rate = this.rate;
    const [first, second] = this.rate > 1 ? [stretch, transposer] : [transposer, stretch];
    first.inputBuffer = ends.inputBuffer;
    first.outputBuffer = middle;
    second.inputBuffer = middle;
    second.outputBuffer = ends.outputBuffer;
    return { input: ends.inputBuffer, output: ends.outputBuffer, transposer, stretch };
  }

  // feed adds n source frames from frame on, interleaved, to every chain.
  feed(tracks, guide, frame, n) {
    const sources = [...tracks, guide];
    for (let c = 0; c < this.chains.length; c++) {
      const { left, right } = sources[c];
      const buf = this.scratch[c];
      for (let i = 0; i < n; i++) {
        buf[2 * i] = left[frame + i] ?? 0;
        buf[2 * i + 1] = (right ?? left)[frame + i] ?? 0;
      }
      this.chains[c].input.putSamples(buf, 0, n);
    }
    const stretches = this.chains.map((c) => c.stretch);
    if (this.rate > 1) {
      linkedStretch(stretches);
      for (const c of this.chains) c.transposer.process();
    } else {
      for (const c of this.chains) c.transposer.process();
      linkedStretch(stretches);
    }
  }

  // available is how many output frames are ready; every chain has the same.
  available() {
    return this.chains[0].output.frameCount;
  }

  // receive writes n output frames of each track from offset o, and drops the guide's.
  receive(outputs, o, n) {
    for (let c = 0; c < this.chains.length; c++) {
      const out = this.chains[c].output;
      if (c < this.chains.length - 1) {
        const v = out.vector;
        const from = out.startIndex;
        const [l, r] = outputs[c];
        for (let i = 0; i < n; i++) {
          l[o + i] = v[from + 2 * i];
          r[o + i] = v[from + 2 * i + 1];
        }
      }
      out.receive(n);
    }
  }

  clear() {
    for (const c of this.chains) {
      c.transposer.clear();
      c.stretch.clear();
    }
  }
}

// linkedStretch is Stretch.process() run over several stretches at once, with
// the overlap position found on the last one (the guide) used for all. They
// get the same number of frames, so they stay in step. It reaches into
// Stretch's internals, which is why the library version is pinned.
function linkedStretch(stretches) {
  for (const s of stretches) {
    if (s.midBuffer !== null) continue;
    if (s._inputBuffer.frameCount < s.overlapLength) return;
    s.midBuffer = new Float32Array(s.overlapLength * 2);
    s._inputBuffer.receiveSamples(s.midBuffer, s.overlapLength);
  }
  const guide = stretches[stretches.length - 1];
  while (guide._inputBuffer.frameCount >= guide.sampleReq) {
    const offset = guide.seekBestOverlapPosition();
    for (const s of stretches) {
      s._outputBuffer.ensureAdditionalCapacity(s.overlapLength);
      s.overlap(offset);
      s._outputBuffer.put(s.overlapLength);
      const rest = s.seekWindowLength - 2 * s.overlapLength;
      if (rest > 0) s._outputBuffer.putBuffer(s._inputBuffer, offset + s.overlapLength, rest);
      const start = s._inputBuffer.startIndex + 2 * (offset + s.seekWindowLength - s.overlapLength);
      s.midBuffer.set(s._inputBuffer.vector.subarray(start, start + 2 * s.overlapLength));
      s.skipFract += s.nominalSkip;
      const skip = Math.floor(s.skipFract);
      s.skipFract -= skip;
      s._inputBuffer.receive(skip);
    }
  }
}

registerProcessor("practice-engine", PracticeEngine);
