package processor

import (
	"errors"
	"testing"
)

func TestCanonicalURL(t *testing.T) {
	const want = "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
	ok := []string{
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://youtube.com/watch?v=dQw4w9WgXcQ&list=PL123&t=42s",
		"youtube.com/watch?v=dQw4w9WgXcQ",
		"  https://m.youtube.com/watch?feature=share&v=dQw4w9WgXcQ  ",
		"https://music.youtube.com/watch?v=dQw4w9WgXcQ&si=abc",
		"https://youtu.be/dQw4w9WgXcQ",
		"https://youtu.be/dQw4w9WgXcQ?si=tracking",
		"https://www.youtube.com/shorts/dQw4w9WgXcQ",
		"https://www.youtube.com/embed/dQw4w9WgXcQ",
		"https://www.youtube.com/live/dQw4w9WgXcQ?feature=share",
		"HTTPS://WWW.YOUTUBE.COM/watch?v=dQw4w9WgXcQ",
	}
	for _, in := range ok {
		got, err := CanonicalURL(in)
		if err != nil || got != want {
			t.Errorf("CanonicalURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}

	bad := map[string]error{
		"https://vimeo.com/123456":                         ErrNotYouTube,
		"https://youtube.com.evil.com/watch?v=dQw4w9WgXcQ": ErrNotYouTube,
		"https://www.youtube.com/playlist?list=PLxxxxxxxx": ErrNoVideoID,
		"https://www.youtube.com/watch?v=short":            ErrNoVideoID,
		"https://www.youtube.com/@somechannel":             ErrNoVideoID,
		"https://youtu.be/":                                ErrNoVideoID,
		"":                                                 ErrNotYouTube,
	}
	for in, wantErr := range bad {
		if _, err := CanonicalURL(in); !errors.Is(err, wantErr) {
			t.Errorf("CanonicalURL(%q) err = %v; want %v", in, err, wantErr)
		}
	}
}
