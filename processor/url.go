package processor

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

var (
	ErrNotYouTube     = errors.New("for now, only YouTube links are supported")
	ErrNoVideoID      = errors.New("couldn't find a video in that link")
	videoIDPattern    = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	youtubeHosts      = map[string]bool{"youtube.com": true, "m.youtube.com": true, "music.youtube.com": true}
	videoPathPrefixes = []string{"/shorts/", "/embed/", "/live/", "/v/"}
)

// CanonicalURL turns any form of YouTube video link (watch, youtu.be, shorts,
// embed, music, with or without tracking params) into
// https://www.youtube.com/watch?v=<id>, so the same video always maps to the same row.
func CanonicalURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", ErrNotYouTube
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")

	var id string
	switch {
	case host == "youtu.be":
		id = strings.Trim(u.Path, "/")
	case youtubeHosts[host]:
		if u.Path == "/watch" {
			id = u.Query().Get("v")
			break
		}
		for _, prefix := range videoPathPrefixes {
			if rest, ok := strings.CutPrefix(u.Path, prefix); ok {
				id, _, _ = strings.Cut(rest, "/")
				break
			}
		}
	default:
		return "", ErrNotYouTube
	}

	if !videoIDPattern.MatchString(id) {
		return "", ErrNoVideoID
	}
	return "https://www.youtube.com/watch?v=" + id, nil
}

// VideoID returns the id of a URL produced by CanonicalURL.
func VideoID(canonicalURL string) string {
	_, id, _ := strings.Cut(canonicalURL, "v=")
	return id
}
