package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/glgaspar/minusOne/db"
)

// Lyrics come from LRCLIB (lrclib.net), a free, community-made database of
// lyrics timed line by line. Its entries can be for a different release of
// the song (a longer intro shifts every line) or plain junk, so the lines are
// only a starting point: the learning pass times the words against the
// song's own vocals.

var lrclibSearchURL = "https://lrclib.net/api/search"

// LRCLIB asks clients to say who they are.
const lrclibUserAgent = "minusOne (self-hosted karaoke, github.com/glgaspar/minusOne)"

var (
	lrclibClient    = &http.Client{Timeout: 20 * time.Second}
	lrclibRetryWait = 3 * time.Second // doubled on the next retry
)

type lrclibEntry struct {
	ID           int64   `json:"id"`
	TrackName    string  `json:"trackName"`
	ArtistName   string  `json:"artistName"`
	Duration     float64 `json:"duration"`
	Instrumental bool    `json:"instrumental"`
	SyncedLyrics string  `json:"syncedLyrics"`
}

// searchLyrics asks LRCLIB for a song's entries. It retries a few times: the
// service answers 503 when it's busy.
func searchLyrics(ctx context.Context, artist, track string) ([]lrclibEntry, error) {
	q := url.Values{"track_name": {track}}
	if artist != "" {
		q.Set("artist_name", artist)
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * lrclibRetryWait):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, lrclibSearchURL+"?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", lrclibUserAgent)
		resp, err := lrclibClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		var entries []lrclibEntry
		err = json.NewDecoder(resp.Body).Decode(&entries)
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusTooManyRequests:
			lastErr = fmt.Errorf("lrclib: %s", resp.Status)
		case resp.StatusCode != http.StatusOK:
			return nil, fmt.Errorf("lrclib: %s", resp.Status)
		case err != nil:
			return nil, fmt.Errorf("lrclib: reading the answer: %w", err)
		default:
			return entries, nil
		}
	}
	return nil, lastErr
}

const (
	// maxDurationGap is how far an entry's length may be from the song's.
	// Videos often run longer than the album track (an intro, an outro).
	maxDurationGap = 15.0
	minLyricLines  = 6
)

// pickLyrics chooses the entry for a song of the given length (seconds): the
// closest in length among those with enough timed lines, or nil. An
// instrumental entry wins only when no entry has lyrics.
func pickLyrics(entries []lrclibEntry, duration float64) *db.Lyrics {
	type candidate struct {
		lyrics *db.Lyrics
		gap    float64
	}
	var withWords, instrumental []candidate
	for _, e := range entries {
		gap := math.Abs(e.Duration - duration)
		if gap > maxDurationGap {
			continue
		}
		if e.Instrumental {
			instrumental = append(instrumental, candidate{&db.Lyrics{Source: "lrclib", LrclibID: e.ID, Instrumental: true}, gap})
			continue
		}
		lines := parseLRC(e.SyncedLyrics, duration)
		if len(lines) < minLyricLines {
			continue
		}
		withWords = append(withWords, candidate{&db.Lyrics{Source: "lrclib", LrclibID: e.ID, Lines: lines}, gap})
	}
	for _, list := range [][]candidate{withWords, instrumental} {
		if len(list) > 0 {
			sort.SliceStable(list, func(i, j int) bool { return list[i].gap < list[j].gap })
			return list[0].lyrics
		}
	}
	return nil
}

var lrcLine = regexp.MustCompile(`^\[(\d+):(\d+(?:\.\d+)?)\]\s*(.*)$`)

// parseLRC reads timed lines ("[01:02.34] words"), skipping empty ones. A
// line lasts until the next starts; the last, until the end of the song.
func parseLRC(lrc string, duration float64) []db.LyricLine {
	var lines []db.LyricLine
	for _, raw := range strings.Split(lrc, "\n") {
		m := lrcLine.FindStringSubmatch(strings.TrimSpace(raw))
		if m == nil {
			continue
		}
		minutes, _ := strconv.Atoi(m[1])
		seconds, _ := strconv.ParseFloat(m[2], 64)
		start := float64(minutes)*60 + seconds
		if n := len(lines); n > 0 && lines[n-1].End == 0 {
			lines[n-1].End = start
		}
		if text := strings.TrimSpace(m[3]); text != "" {
			lines = append(lines, db.LyricLine{Start: start, Text: text})
		}
	}
	if n := len(lines); n > 0 && lines[n-1].End == 0 {
		lines[n-1].End = math.Max(duration, lines[n-1].Start+5)
	}
	return lines
}

var (
	// words that only describe the upload, in (...) or [...] after the title
	uploadNoise = regexp.MustCompile(`(?i)\s*[(\[【][^)\]】]*\b(official|video|vídeo|audio|áudio|lyrics?|letra|legendad[oa]|tradução|clipe|clip|remaster(ed)?|hd|hq|4k|mv|m/v|visuali[sz]er|ao vivo|live)\b[^)\]】]*[)\]】]`)
	// the same, unbracketed, at the end: "Title M/V", "Title - DVD Ao Vivo No Ibirapuera"
	trailingNoise = regexp.MustCompile(`(?i)(\s+[-–—|]\s+[^-–—|]*\b(dvd|ao vivo|live|ac[uú]stico|official|video|clipe|lyrics?|letra|remaster(ed)?)\b.*|\s+(m/v|mv|official (music )?video|lyric video|clipe oficial))$`)
	featuring     = regexp.MustCompile(`(?i)\s+(feat\.?|ft\.?|featuring|part\.)\s.*$`)
	channelJunk   = regexp.MustCompile(`(?i)(vevo|\s+-\s+topic|\s*official)$`)
	dashes        = regexp.MustCompile(`\s+[-–—|]\s+`)
)

// lookupTerms turns a video's title and channel into the artist and track
// to search for: "Artist - Title (Official Video)" is the usual shape; when
// the title has no artist, the channel is the artist.
func lookupTerms(title, channel string) (artist, track string) {
	t := uploadNoise.ReplaceAllString(title, "")
	t = trailingNoise.ReplaceAllString(t, "")
	t = strings.Trim(strings.TrimSpace(t), `"“”`)
	if parts := dashes.Split(t, 2); len(parts) == 2 {
		artist, track = parts[0], parts[1]
	} else {
		artist, track = strings.TrimSpace(channelJunk.ReplaceAllString(channel, "")), t
	}
	artist = strings.TrimSpace(featuring.ReplaceAllString(artist, ""))
	track = strings.Trim(strings.TrimSpace(featuring.ReplaceAllString(track, "")), `"“”`)
	return artist, track
}

// findLyrics looks the song up on LRCLIB and saves the lyrics it picks. It
// returns whether it saved any. Nothing found isn't an error.
func (s *Song) findLyrics(ctx context.Context, database *db.DB) (bool, error) {
	artist, track := lookupTerms(s.Title, s.Author)
	entries, err := searchLyrics(ctx, artist, track)
	if err != nil {
		return false, err
	}
	lyrics := pickLyrics(entries, s.Duration)
	if lyrics == nil {
		return false, nil
	}
	return true, database.PutAnnotation(ctx, s.ID, db.LyricsKind, lyrics)
}
