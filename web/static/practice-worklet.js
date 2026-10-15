// The practice page's audio engine, running on the audio thread.
//
// It holds every track's samples and plays them all in the same process()
// call, one stereo output per track, so the tracks can't drift apart. Volume
// and mute are GainNodes on the page, after the outputs.
//
// It is the clock: it owns the position and reports it to the page, which
// interpolates between reports.

const REPORT_EVERY_S = 0.05;

class PracticeEngine extends AudioWorkletProcessor {
  constructor() {
    super();
    this.tracks = []; // [{ left, right }] Float32Arrays; right is null for mono
    this.length = 0; // in frames
    this.pos = 0; // the next source frame to play
    this.playing = false;
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
        break;
      case "pause":
        this.playing = false;
        break;
      case "seek":
        this.pos = Math.min(Math.max(Math.round(m.position * sampleRate), 0), this.length);
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
      time,
    });
  }

  process(inputs, outputs) {
    if (!this.playing) return true; // outputs stay silent

    const frames = outputs[0][0].length;
    const n = Math.min(frames, this.length - this.pos);
    for (let t = 0; t < this.tracks.length; t++) {
      const { left, right } = this.tracks[t];
      const out = outputs[t];
      out[0].set(left.subarray(this.pos, this.pos + n));
      out[1].set((right ?? left).subarray(this.pos, this.pos + n));
    }
    this.pos += n;

    const end = currentTime + frames / sampleRate;
    if (this.pos >= this.length) {
      this.playing = false;
      this.report(end);
      this.port.postMessage({ type: "ended" });
    } else if (end - this.lastReport >= REPORT_EVERY_S) {
      this.report(end);
    }
    return true;
  }
}

registerProcessor("practice-engine", PracticeEngine);
