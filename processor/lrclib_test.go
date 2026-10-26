package processor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLookupTerms(t *testing.T) {
	tests := []struct{ title, channel, artist, track string }{
		{"Rick Astley - Never Gonna Give You Up (Official Video) (4K Remaster)", "Rick Astley", "Rick Astley", "Never Gonna Give You Up"},
		{"Chitãozinho & Xororó - Evidências", "Chitãozinho & Xororó", "Chitãozinho & Xororó", "Evidências"},
		{"PSY - GANGNAM STYLE(강남스타일) M/V", "officialpsy", "PSY", "GANGNAM STYLE(강남스타일)"},
		{"Queen – Bohemian Rhapsody (Official Video Remastered)", "Queen Official", "Queen", "Bohemian Rhapsody"},
		{"Marília Mendonça - Infiel (Ao Vivo)", "Marília Mendonça", "Marília Mendonça", "Infiel"},
		{"Imagine Dragons - Believer (Lyrics)", "7clouds", "Imagine Dragons", "Believer"},
		{"Ed Sheeran - Shape of You [Official Video]", "Ed Sheeran", "Ed Sheeran", "Shape of You"},
		{"Shape of You", "Ed Sheeran - Topic", "Ed Sheeran", "Shape of You"},
		{"Bad Romance", "LadyGagaVEVO", "LadyGaga", "Bad Romance"},
		{"Henrique e Juliano - Liberdade Provisória - DVD Ao Vivo No Ibirapuera", "Henrique e Juliano", "Henrique e Juliano", "Liberdade Provisória"},
		{"Tim Maia - Gostava Tanto de Você (Clipe Oficial)", "Tim Maia", "Tim Maia", "Gostava Tanto de Você"},
		{"Jorge & Mateus - Propaganda (Ao Vivo) [Official Video]", "Jorge & Mateus", "Jorge & Mateus", "Propaganda"},
		{"Shakira ft. Freshlyground - Waka Waka", "shakiraVEVO", "Shakira", "Waka Waka"},
		{"Calvin Harris - This Is What You Came For (Official Video) ft. Rihanna", "CalvinHarrisVEVO", "Calvin Harris", "This Is What You Came For"},
		{"The Rolling Stones - (I Can't Get No) Satisfaction (Official Lyric Video)", "The Rolling Stones", "The Rolling Stones", "(I Can't Get No) Satisfaction"},
		{`Djavan - "Oceano" (Ao Vivo)`, "Djavan", "Djavan", "Oceano"},
	}
	for _, tt := range tests {
		artist, track := lookupTerms(tt.title, tt.channel)
		if artist != tt.artist || track != tt.track {
			t.Errorf("lookupTerms(%q, %q) = %q, %q; want %q, %q", tt.title, tt.channel, artist, track, tt.artist, tt.track)
		}
	}
}

func TestParseLRC(t *testing.T) {
	lines := parseLRC("[00:00.00] \n[00:18.92] We're no strangers to love\n[00:22.59]You know the rules\n[00:26.93]\n[01:02.5] Never gonna\nnot a line\n", 213)
	want := []struct {
		start, end float64
		text       string
	}{{18.92, 22.59, "We're no strangers to love"}, {22.59, 26.93, "You know the rules"}, {62.5, 213, "Never gonna"}}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines: %+v", len(lines), lines)
	}
	for i, w := range want {
		if l := lines[i]; l.Start != w.start || l.End != w.end || l.Text != w.text {
			t.Errorf("line %d = %+v, want %+v", i, l, w)
		}
	}
}

func lrc(n int) string {
	s := ""
	for i := 0; i < n; i++ {
		s += "[00:" + string(rune('1'+i)) + "0.00] line\n"
	}
	return s
}

func TestPickLyrics(t *testing.T) {
	entries := []lrclibEntry{
		{ID: 1, Duration: 214, SyncedLyrics: "[00:00.00]probe"},      // junk
		{ID: 2, Duration: 240, SyncedLyrics: lrc(8)},                 // another version, 27 s longer
		{ID: 3, Duration: 220, SyncedLyrics: lrc(8)},                 // fine, 7 s off
		{ID: 4, Duration: 211, SyncedLyrics: lrc(8)},                 // the closest
		{ID: 5, Duration: 213, Instrumental: true},                   // instrumental loses to lyrics
		{ID: 6, Duration: 213, SyncedLyrics: "", TrackName: "plain"}, // no timing
	}
	if got := pickLyrics(entries, 213); got == nil || got.LrclibID != 4 || len(got.Lines) != 8 || got.Source != "lrclib" {
		t.Errorf("picked %+v, want entry 4", got)
	}
	if got := pickLyrics(entries[:2], 213); got != nil {
		t.Errorf("junk and far entries: picked %+v", got)
	}
	if got := pickLyrics(entries[4:], 213); got == nil || !got.Instrumental || got.LrclibID != 5 {
		t.Errorf("instrumental only: picked %+v", got)
	}
}

func TestSearchLyricsRetriesWhenBusy(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("User-Agent") != lrclibUserAgent {
			t.Errorf("User-Agent %q", r.Header.Get("User-Agent"))
		}
		if q := r.URL.Query(); q.Get("track_name") != "Evidências" || q.Get("artist_name") != "Chitãozinho & Xororó" {
			t.Errorf("query %v", q)
		}
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"message":"The server is busy, please retry in a moment","statusCode":503}`))
			return
		}
		json.NewEncoder(w).Encode([]lrclibEntry{{ID: 7, Duration: 281, SyncedLyrics: lrc(6)}})
	}))
	defer srv.Close()
	defer func(u string, wait time.Duration) { lrclibSearchURL, lrclibRetryWait = u, wait }(lrclibSearchURL, lrclibRetryWait)
	lrclibSearchURL, lrclibRetryWait = srv.URL, time.Millisecond

	entries, err := searchLyrics(context.Background(), "Chitãozinho & Xororó", "Evidências")
	if err != nil || len(entries) != 1 || entries[0].ID != 7 || calls != 2 {
		t.Errorf("entries %+v, err %v, calls %d", entries, err, calls)
	}
}
