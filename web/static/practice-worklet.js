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

const REPORT_EVERY_S = 0.05;
const CLICK_S = 0.03;

class PracticeEngine extends AudioWorkletProcessor {
  constructor() {
    super();
    this.tracks = []; // [{ left, right }] Float32Arrays; right is null for mono
    this.length = 0; // in frames
    this.pos = 0; // the next source frame to play
    this.playing = false;
    this.loop = null; // { a, b } in frames, while looping
    this.countIn = 0; // frames of count-in
    this.counting = 0; // frames of count-in left
    this.seq = 0; // the page's last command, echoed so it can drop stale reports
    this.lastReport = -Infinity;
    this.port.onmessage = (e) => this.command(e.data);
  }

  command(m) {
    this.seq = m.seq ?? this.seq;
    switch (m.type) {
      case "load":
        this.tracks = m.tracks;
        this.length = Math.max(0, ...m.tracks.map((t) => t.left.length));
        this.pos = 0;
        break;
      case "play":
        if (this.pos >= this.length) this.pos = 0;
        this.playing = this.length > 0;
        this.counting = this.countIn;
        break;
      case "pause":
        this.playing = false;
        this.counting = 0;
        break;
      case "seek":
        this.pos = Math.min(Math.max(Math.round(m.position * sampleRate), 0), this.length);
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
    }
    this.report(currentTime);
  }

  report(time) {
    this.lastReport = time;
    this.port.postMessage({
      type: "state",
      seq: this.seq,
      position: this.pos / sampleRate,
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
      const looping = this.loop && this.pos < this.loop.b;
      const end = looping ? this.loop.b : this.length;
      const n = Math.min(frames - o, end - this.pos);
      for (let t = 0; t < this.tracks.length; t++) {
        const { left, right } = this.tracks[t];
        outputs[t][0].set(left.subarray(this.pos, this.pos + n), o);
        outputs[t][1].set((right ?? left).subarray(this.pos, this.pos + n), o);
      }
      this.pos += n;
      o += n;

      if (this.pos < end) continue;
      if (looping) {
        this.pos = this.loop.a;
        this.counting = this.countIn;
        this.lastReport = -Infinity; // report the jump right away
      } else {
        this.playing = false;
        this.port.postMessage({ type: "ended" });
      }
    }

    const time = currentTime + frames / sampleRate;
    if (!this.playing || time - this.lastReport >= REPORT_EVERY_S) this.report(time);
    return true;
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

registerProcessor("practice-engine", PracticeEngine);
