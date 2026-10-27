-- Songs learned before chords were found get their learning pass again, which
-- finds them. Any pass ends with chords, so this only matches those songs.
UPDATE songs SET learning_status = 'pending', learning_attempts = 0
WHERE status = 'done' AND learning_status = 'done' AND NOT EXISTS (
    SELECT 1 FROM song_annotations a WHERE a.song_id = songs.id AND a.kind = 'chords');
