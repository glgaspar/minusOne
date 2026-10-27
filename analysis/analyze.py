#!/usr/bin/env python3
"""minusone-analyze: the processor's audio analysis, run once per job.

    minusone-analyze align <vocals audio> <lines.json>
    minusone-analyze transcribe <vocals audio>

align times lyrics word by word against a song's vocals track. lines.json is
a list of {"text": ...}, the lyrics line by line. It writes
{"score": ..., "lines": [{"start", "end", "text", "words": [{"start", "end",
"text"}]}]} to stdout, in seconds. score is the median log-likelihood per
20 ms of the words: around -1 or above when the lyrics are the song's, well
below when they aren't.

transcribe writes the lyrics it hears in the vocals, for songs whose lyrics
weren't found: {"language": "pt", "lines": [{"text": ...}]}. Untimed: align
times them, more precisely than the transcription would.

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


COMMANDS = {"align": (align, 2), "transcribe": (transcribe, 1)}

if __name__ == "__main__":
    if len(sys.argv) < 2 or sys.argv[1] not in COMMANDS or len(sys.argv) - 2 != COMMANDS[sys.argv[1]][1]:
        sys.exit(__doc__)
    command, _ = COMMANDS[sys.argv[1]]
    command(*sys.argv[2:])
