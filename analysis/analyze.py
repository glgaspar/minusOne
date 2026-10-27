#!/usr/bin/env python3
"""minusone-analyze: the processor's audio analysis, run once per job.

    minusone-analyze align <vocals audio> <lines.json>
    minusone-analyze transcribe <vocals audio>
    minusone-analyze chords <instrumental audio>

align times lyrics word by word against a song's vocals track. lines.json is
a list of {"text": ...}, the lyrics line by line. It writes
{"score": ..., "lines": [{"start", "end", "text", "words": [{"start", "end",
"text"}]}]} to stdout, in seconds. score is the median log-likelihood per
20 ms of the words: around -1 or above when the lyrics are the song's, well
below when they aren't.

transcribe writes the lyrics it hears in the vocals, for songs whose lyrics
weren't found: {"language": "pt", "lines": [{"text": ...}]}. Untimed: align
times them, more precisely than the transcription would.

chords finds a song's beats, chords and key, best from its instrumental:
{"key": {"root": 0-11, "mode": "major"|"minor"}, "tempo": bpm,
"beats": [seconds], "chords": [{"start", "end", "root": 0-11, "quality"}]},
with C as 0 and qualities "", "m", "7", "m7" and "maj7". Stretches with no
chord are left out.

Errors go to stderr, with the reason on the last line. Models download on
first use into $MINUSONE_MODELS.
"""

import json
import os
import sys
import tempfile

MODELS = os.environ.get("MINUSONE_MODELS") or os.path.join(tempfile.gettempdir(), "minusone-models")
# leave some of the CPU to the web server
THREADS = max(1, (os.cpu_count() or 2) - 2)
# how long a line stays up after its last word, unless the next one starts
LINE_HOLD = 1.5
# Whisper's "small": good on sung Portuguese and English, ~0.2x the song's
# length on a laptop CPU, 1 GB of memory
WHISPER = "small"
# a transcribed line ends after this many words
LINE_WORDS = 10
# capitalized words that don't start a phrase
NOT_PHRASE_START = {"I", "I'm", "I'll", "I've", "I'd", "Deus", "God"}


def model_file(name, url):
    """Returns the path of a model file, downloading it the first time. It
    downloads to a temporary name, so a download cut short is never taken for
    the model."""
    import requests

    path = os.path.join(MODELS, name)
    if os.path.exists(path):
        return path
    os.makedirs(os.path.dirname(path), exist_ok=True)
    partial = path + ".download"
    with requests.get(url, stream=True, timeout=60) as r:
        r.raise_for_status()
        with open(partial, "wb") as f:
            for chunk in r.iter_content(1 << 20):
                f.write(chunk)
    os.rename(partial, path)
    return path


def align(vocals, lines_path):
    # ctc-forced-aligner: the MMS forced-alignment model on onnxruntime. Text
    # is romanized (unidecode), so it works the same for any Latin language
    # and roughly for others.
    import numpy as np
    import onnxruntime
    import ctc_forced_aligner as cfa

    with open(lines_path) as f:
        texts = [line["text"] for line in json.load(f)]
    line_words = [t.split() for t in texts]
    if not any(line_words):
        sys.exit("no words to align")

    options = onnxruntime.SessionOptions()
    options.intra_op_num_threads = THREADS
    session = onnxruntime.InferenceSession(model_file("aligner/mms-300m.onnx", cfa.MODEL_URL), options)
    # batches of 2 windows of 30 s: 4 runs the same speed with 2 GB more memory
    emissions, stride = cfa.generate_emissions(session, cfa.load_audio(vocals), batch_size=2)
    tokens, starred = cfa.preprocess_text(" ".join(" ".join(w) for w in line_words), romanize=True, language="und")
    segments, scores, blank = cfa.get_alignments(emissions, tokens, cfa.Tokenizer())
    words = cfa.postprocess_results(starred, cfa.get_spans(tokens, segments, blank), stride, scores)
    if len(words) != sum(map(len, line_words)):
        sys.exit(f"aligned {len(words)} words of {sum(map(len, line_words))}")

    lines, per_frame, i = [], [], 0
    for text, ws in zip(texts, line_words):
        aligned = words[i : i + len(ws)]
        i += len(ws)
        if not aligned:
            continue
        lines.append({
            "start": round(aligned[0]["start"], 3),
            "end": round(aligned[-1]["end"], 3),
            "text": text,
            "words": [{"start": round(w["start"], 3), "end": round(w["end"], 3), "text": w["text"]} for w in aligned],
        })
        per_frame += [w["score"] / max(1, round((w["end"] - w["start"]) * 50)) for w in aligned]
    for line, following in zip(lines, lines[1:] + [None]):
        hold = line["end"] + LINE_HOLD
        line["end"] = round(max(line["end"], min(hold, following["start"]) if following else hold), 3)

    json.dump({"score": round(float(np.median(per_frame)), 3), "lines": lines}, sys.stdout, ensure_ascii=False)


def transcribe(vocals):
    import soundfile
    from faster_whisper import WhisperModel

    model = WhisperModel(WHISPER, device="cpu", compute_type="int8", cpu_threads=THREADS,
                         download_root=os.path.join(MODELS, "whisper"))
    # 16 kHz mono, decoded by the caller: given a file, faster-whisper 1.2
    # decodes it with PyAV and fails on its current versions
    audio, _ = soundfile.read(vocals, dtype="float32")
    segments, info = model.transcribe(audio, word_timestamps=True, vad_filter=True,
                                      condition_on_previous_text=False)

    # a line per segment, but Whisper's segments often hold two sung lines:
    # break them where a new phrase starts too. (Not at pauses: singers hold
    # notes before a line's last word.)
    lines, words = [], []
    for segment in segments:
        # Whisper's own test for music it heard words in. no_speech_prob
        # alone is high on most singing, even when the words are right.
        if segment.no_speech_prob > 0.6 and segment.avg_logprob < -1:
            continue
        for i, w in enumerate(segment.words):
            text = w.word.strip()
            if not text:
                continue
            new_phrase = text[0].isupper() and text not in NOT_PHRASE_START and len(words) >= 3
            if words and (i == 0 or new_phrase or len(words) >= LINE_WORDS):
                lines.append({"text": " ".join(words)})
                words = []
            words.append(text)
    if words:
        lines.append({"text": " ".join(words)})
    json.dump({"language": info.language, "lines": lines}, sys.stdout, ensure_ascii=False)


# chord templates: intervals above the root
QUALITIES = {"": (0, 4, 7), "m": (0, 3, 7), "7": (0, 4, 7, 10), "m7": (0, 3, 7, 10), "maj7": (0, 4, 7, 11)}
# Krumhansl-Kessler key profiles, from C
MAJOR = (6.35, 2.23, 3.48, 2.33, 4.38, 4.09, 2.52, 5.19, 2.39, 3.66, 2.29, 2.88)
MINOR = (6.33, 2.68, 3.52, 5.38, 2.60, 3.53, 2.54, 4.75, 3.98, 2.69, 3.34, 3.17)


def chords(audio):
    # Chroma (how much of each of the 12 notes sounds) per beat, matched
    # against chord templates, smoothed with Viterbi so a chord lasts unless
    # the music clearly moves. Major and minor are reliable; 7ths less.
    import numpy as np
    import librosa

    y, sr = librosa.load(audio, sr=22050, mono=True)
    hop = 512
    tempo, beats = librosa.beat.beat_track(y=y, sr=sr, hop_length=hop)
    chroma = librosa.feature.chroma_cqt(y=librosa.effects.harmonic(y, margin=4), sr=sr, hop_length=hop, bins_per_octave=36)
    bounds = np.concatenate([[0], beats, [chroma.shape[1]]]).astype(int)
    per_beat = librosa.util.sync(chroma, bounds, aggregate=np.median)
    times = librosa.frames_to_time(bounds, sr=sr, hop_length=hop)

    states = [None]  # no chord
    templates = [np.full(12, 1 / np.sqrt(12))]
    for quality, intervals in QUALITIES.items():
        for root in range(12):
            t = np.zeros(12)
            t[[(root + i) % 12 for i in intervals]] = 1
            # a triad wins unless the 7th is clearly there
            templates.append(t / np.linalg.norm(t) * (0.97 if len(intervals) == 4 else 1))
            states.append((root, quality))
    energy = per_beat.sum(axis=0)
    quiet = energy < 0.05 * np.median(energy)
    similarity = np.array(templates) @ (per_beat / np.maximum(np.linalg.norm(per_beat, axis=0), 1e-9))
    similarity[0] = 0.55  # "no chord" wins when nothing fits well
    similarity[:, quiet] = 0
    similarity[0, quiet] = 1
    likelihood = np.exp(similarity * 25)
    likelihood /= likelihood.sum(axis=0, keepdims=True)
    path = librosa.sequence.viterbi(likelihood, librosa.sequence.transition_loop(len(states), 0.8))

    found = []
    for i, s in enumerate(path):
        start, end = round(float(times[i]), 3), round(float(times[i + 1]), 3)
        if found and found[-1]["state"] == s and found[-1]["end"] == start:
            found[-1]["end"] = end
        elif states[s] is not None:
            found.append({"state": s, "start": start, "end": end})
    mean = chroma.mean(axis=1)
    key = max(((np.corrcoef(np.roll(profile, r), mean)[0, 1], r, mode)
               for mode, profile in (("major", MAJOR), ("minor", MINOR)) for r in range(12)))
    json.dump({
        "key": {"root": key[1], "mode": key[2]},
        "tempo": round(float(np.atleast_1d(tempo)[0]), 1),
        "beats": [round(float(t), 3) for t in librosa.frames_to_time(beats, sr=sr, hop_length=hop)],
        "chords": [{"start": c["start"], "end": c["end"], "root": states[c["state"]][0], "quality": states[c["state"]][1]} for c in found],
    }, sys.stdout)


COMMANDS = {"align": (align, 2), "transcribe": (transcribe, 1), "chords": (chords, 1)}

if __name__ == "__main__":
    if len(sys.argv) < 2 or sys.argv[1] not in COMMANDS or len(sys.argv) - 2 != COMMANDS[sys.argv[1]][1]:
        sys.exit(__doc__)
    command, _ = COMMANDS[sys.argv[1]]
    command(*sys.argv[2:])
