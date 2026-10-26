package processor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeTools puts stand-ins for ffmpeg and audio-separator first on PATH.
// ffmpeg writes its last argument; audio-separator writes <name>.flac for each
// name in --custom_output_names, except a stem named in $SKIP, or fails with
// $FAIL as its last words.
func fakeTools(t *testing.T) {
	t.Setenv("MINUSONE_MODELS", t.TempDir())
	dir := t.TempDir()
	scripts := map[string]string{
		"ffmpeg": `for out; do :; done; echo audio > "$out"`,
		"audio-separator": `
[ -n "$FAIL" ] && { echo "Traceback..." >&2; echo "$FAIL" >&2; exit 1; }
while [ $# -gt 0 ]; do
  case "$1" in
    --output_dir) out=$2; shift ;;
    --model_file_dir) mkdir -p "$2"; echo "$model" > "$2/model"; shift ;;
    --model_filename) model=$2; shift ;;
    --download_model_only) only=1 ;;
    --custom_output_names) names=$2; shift ;;
  esac
  shift
done
[ -n "$only" ] && exit 0
mkdir -p "$out"
for n in $(echo "$names" | grep -o ':"[a-z]*"' | tr -d ':"'); do
  [ "$n" = "$SKIP" ] || echo stem > "$out/$n.flac"
done`,
	}
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestModelsDownloadOnceAndWhole(t *testing.T) {
	fakeTools(t)
	base := t.TempDir()
	t.Setenv("MINUSONE_MODELS", base)

	// a download cut short last time, and a file in the model's place
	if err := os.MkdirAll(filepath.Join(base, "k.onnx.download"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "k.onnx"), []byte("half a model"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir, err := modelDir(context.Background(), "k.onnx")
	if err != nil || dir != filepath.Join(base, "k.onnx") {
		t.Fatalf("modelDir = %q, %v", dir, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "model")); err != nil {
		t.Errorf("not downloaded: %v", err)
	}
	if _, err := os.Stat(dir + ".download"); !os.IsNotExist(err) {
		t.Errorf("partial download left behind: %v", err)
	}

	// downloaded once: a failing separator isn't called again
	t.Setenv("FAIL", "no network")
	if again, err := modelDir(context.Background(), "k.onnx"); err != nil || again != dir {
		t.Errorf("second call = %q, %v", again, err)
	}
}

func newTestSong(t *testing.T) *Song {
	s := &Song{ID: "1", Extension: ".webm", WorkDir: t.TempDir()}
	if err := os.WriteFile(s.OriginalPath(), []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSplitKeepsThePassStems(t *testing.T) {
	fakeTools(t)
	models := Models{Karaoke: "k.onnx", Learning: "l.yaml"}

	tests := []struct {
		pass pass
		want string
	}{
		{karaokePass(models), "instrumental,vocals"},
		{learningPass(models), "bass,drums,guitar,other,piano"},
	}
	for _, tt := range tests {
		s := newTestSong(t)
		if err := s.split(context.Background(), tt.pass); err != nil {
			t.Fatalf("%s: %v", tt.pass.model, err)
		}
		var types []string
		for _, tr := range s.Tracks {
			types = append(types, tr.Type)
			if tr.Extension != ".webm" || tr.Path != filepath.Join(s.WorkDir, tr.Type+".webm") {
				t.Errorf("%s: track %+v", tt.pass.model, tr)
			}
			if _, err := os.Stat(tr.Path); err != nil {
				t.Errorf("%s: %v", tt.pass.model, err)
			}
		}
		if got := strings.Join(types, ","); got != tt.want {
			t.Errorf("%s: tracks %s, want %s", tt.pass.model, got, tt.want)
		}
	}
}

func TestSplitErrors(t *testing.T) {
	fakeTools(t)
	p := karaokePass(Models{Karaoke: "k.onnx"})
	if _, err := modelDir(context.Background(), "k.onnx"); err != nil {
		t.Fatal(err)
	}

	t.Setenv("FAIL", "BatchSeparationError: Separation failed")
	err := newTestSong(t).split(context.Background(), p)
	if err == nil || !strings.Contains(err.Error(), "separating with k.onnx: BatchSeparationError: Separation failed") {
		t.Errorf("separator failure: %v", err)
	}

	t.Setenv("FAIL", "")
	t.Setenv("SKIP", "vocals")
	err = newTestSong(t).split(context.Background(), p)
	if err == nil || err.Error() != "k.onnx gave no vocals track" {
		t.Errorf("missing stem: %v", err)
	}
}
