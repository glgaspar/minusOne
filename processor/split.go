package processor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"github.com/glgaspar/minusOne/db"
)

// Tracks are split by audio-separator (github.com/nomadkaraoke/python-audio-separator),
// a Python CLI that runs the separation models. Like yt-dlp, it must be on
// PATH, along with ffmpeg.
//
// A song goes through two passes:
//   - karaoke: vocals and instrumental, right after the download. The song is
//     Ready after it.
//   - learning: the instruments, for practicing. It only runs while no song
//     waits for its karaoke pass (see Run).
//
// Both run on the CPU and take minutes per song, so they run under nice: the
// web server stays responsive.

// Models names the separation model of each pass, as audio-separator knows them.
type Models struct {
	Karaoke  string // MINUSONE_KARAOKE_MODEL
	Learning string // MINUSONE_LEARNING_MODEL
}

// passTimeout bounds one separation. On a laptop CPU a pass takes 1-2x the
// song's length, so this fits songs well past 10 minutes.
const passTimeout = 40 * time.Minute

// pass is one run of a model and the stems kept from it: the model's name for
// each stem mapped to the track type it's saved as.
type pass struct {
	model string
	stems map[string]string
}

func karaokePass(m Models) pass {
	return pass{m.Karaoke, map[string]string{"Vocals": "vocals", "Instrumental": "instrumental"}}
}

// learningPass leaves out the model's vocals: the karaoke pass's are cleaner.
func learningPass(m Models) pass {
	return pass{m.Learning, map[string]string{
		"Drums": "drums", "Bass": "bass", "Guitar": "guitar", "Piano": "piano", "Other": "other",
	}}
}

// split runs p on the original in WorkDir and appends the kept stems to
// s.Tracks, encoded for the browser.
func (s *Song) split(ctx context.Context, p pass) error {
	ctx, cancel := context.WithTimeout(ctx, passTimeout)
	defer cancel()

	// Decoded first: the separator's own decoding doesn't read every format
	// YouTube serves (webm, for one). 44.1 kHz is what the models work at;
	// float keeps peaks above 0 dB.
	input := filepath.Join(s.WorkDir, "input.wav")
	if err := run(ctx, "ffmpeg", "-v", "error", "-y", "-i", s.OriginalPath(),
		"-ar", "44100", "-ac", "2", "-c:a", "pcm_f32le", input); err != nil {
		return fmt.Errorf("decoding the original: %w", err)
	}

	names, err := json.Marshal(p.stems)
	if err != nil {
		return err
	}
	models, err := modelDir(ctx, p.model)
	if err != nil {
		return err
	}
	outDir := filepath.Join(s.WorkDir, "stems")
	err = run(ctx, "nice", "-n", "10", "audio-separator", input,
		"--model_filename", p.model,
		"--model_file_dir", models,
		"--output_dir", outDir,
		"--output_format", "FLAC",
		// The default rescales any stem peaking over 0.9 on its own, which
		// would change the balance between tracks. 1 only touches a stem
		// that would clip.
		"--normalization", "1",
		"--custom_output_names", string(names),
		"--log_level", "warning")
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("separating with %s took longer than %s", p.model, passTimeout)
	}
	if err != nil {
		return fmt.Errorf("separating with %s: %w", p.model, err)
	}

	types := make([]string, 0, len(p.stems))
	for _, t := range p.stems {
		types = append(types, t)
	}
	sort.Strings(types)
	for _, t := range types {
		stem := filepath.Join(outDir, t+".flac")
		if _, err := os.Stat(stem); err != nil {
			return fmt.Errorf("%s gave no %s track", p.model, t)
		}
		// Opus in WebM, like most YouTube originals: small, and its start
		// padding is in the header, so decoders drop it and the tracks stay
		// aligned with the original (AAC's isn't, always).
		track := Track{Type: t, Extension: ".webm", Path: filepath.Join(s.WorkDir, t+".webm")}
		if err := run(ctx, "ffmpeg", "-v", "error", "-y", "-i", stem,
			"-c:a", "libopus", "-b:a", "160k", track.Path); err != nil {
			return fmt.Errorf("encoding %s: %w", t, err)
		}
		s.Tracks = append(s.Tracks, track)
	}
	return nil
}

// modelDir returns the directory holding a model's files, downloading them the
// first time. audio-separator takes any file already there as downloaded, so
// a download cut short (a pass that gave way, a restart) would leave a broken
// model for good. So each model downloads into a directory of its own, which
// only gets its name once the download is complete.
func modelDir(ctx context.Context, model string) (string, error) {
	base := os.Getenv("AUDIO_SEPARATOR_MODEL_DIR") // the separator's own setting
	if base == "" {
		base = filepath.Join(os.TempDir(), "audio-separator-models")
	}
	dir := filepath.Join(base, model)
	if _, err := os.Stat(dir); err == nil {
		return dir, nil
	}
	partial := dir + ".download"
	if err := os.RemoveAll(partial); err != nil {
		return "", err
	}
	if err := run(ctx, "audio-separator", "--download_model_only", "--model_filename", model,
		"--model_file_dir", partial, "--log_level", "warning"); err != nil {
		return "", fmt.Errorf("downloading %s: %w", model, err)
	}
	return dir, os.Rename(partial, dir)
}

// splitAndSave runs a pass and stores its tracks.
func (s *Song) splitAndSave(ctx context.Context, p pass, database *db.DB, store *Store) error {
	if err := s.split(ctx, p); err != nil {
		return fmt.Errorf("splitting: %w", err)
	}
	if err := s.UploadTracks(ctx, store); err != nil {
		return fmt.Errorf("uploading tracks: %w", err)
	}
	return s.SaveToDatabase(ctx, database)
}

// fetchOriginal copies the stored original into WorkDir, for passes that run
// after the download.
func (s *Song) fetchOriginal(ctx context.Context, store *Store) error {
	obj, err := store.Get(ctx, s.OriginalKey)
	if err != nil {
		return fmt.Errorf("fetching the original: %w", err)
	}
	defer obj.Close()
	file, err := os.Create(s.OriginalPath())
	if err != nil {
		return err
	}
	if _, err := io.Copy(file, obj); err != nil {
		file.Close()
		return fmt.Errorf("fetching the original: %w", err)
	}
	return file.Close()
}

// run runs a command and turns a failure into the last line it wrote to
// stderr, which is where these tools say what went wrong.
func run(ctx context.Context, name string, args ...string) error {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ProcessState.Sys().(syscall.WaitStatus).Signaled() && ctx.Err() == nil {
			// stderr ends in a progress bar then; the kernel killing it for
			// memory is the usual reason
			return fmt.Errorf("%s was killed (out of memory?)", name)
		}
		if msg := lastLine(stderr.String()); msg != "" {
			return errors.New(msg)
		}
		return fmt.Errorf("running %s: %w", name, err)
	}
	return nil
}
