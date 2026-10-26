package web

import (
	"strings"
	"testing"

	"github.com/glgaspar/minusOne/db"
)

func TestPlayerSongStartsWithOneBacking(t *testing.T) {
	key := "songs/1/original.webm"
	song := func(types ...string) *db.Song {
		s := &db.Song{ID: "1", SourceURL: "https://www.youtube.com/watch?v=aaaaaaaaaaa", OriginalKey: &key}
		for _, typ := range types {
			s.Tracks = append(s.Tracks, db.Track{Type: typ})
		}
		return s
	}
	// the tracks that start unmuted, in mixer order
	unmuted := func(ps *playerSong) string {
		var on []string
		for _, tr := range ps.Tracks {
			if !tr.Muted {
				on = append(on, tr.Type)
			}
		}
		return strings.Join(on, ",")
	}
	karaoke := []string{"vocals", "instrumental"}
	both := []string{"vocals", "instrumental", "drums", "bass", "guitar", "piano", "other"}

	tests := []struct {
		name     string
		song     *db.Song
		practice bool
		want     string
	}{
		{"not split, TV", song(), false, "original"},
		{"not split, practice", song(), true, "original"},
		{"karaoke tracks, TV", song(karaoke...), false, "instrumental"},
		{"karaoke tracks, practice", song(karaoke...), true, "vocals,instrumental"},
		{"all tracks, TV", song(both...), false, "instrumental"},
		{"all tracks, practice", song(both...), true, "vocals,drums,bass,guitar,piano,other"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := unmuted(newPlayerSong(tt.song, tt.practice)); got != tt.want {
				t.Errorf("unmuted = %q, want %q", got, tt.want)
			}
		})
	}
}
