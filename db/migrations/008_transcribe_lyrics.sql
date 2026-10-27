-- Songs learned before lyrics were transcribed: those LRCLIB had none for, or
-- whose lyrics didn't fit the vocals, get their learning pass again, which
-- transcribes them. Any pass ends with lyrics timed or instrumental, so this
-- only matches those songs.
UPDATE songs SET learning_status = 'pending', learning_attempts = 0
WHERE status = 'done' AND learning_status = 'done' AND NOT EXISTS (
    SELECT 1 FROM song_annotations a
    WHERE a.song_id = songs.id AND a.kind = 'lyrics'
      AND (a.data->>'wordSynced' = 'true' OR a.data->>'instrumental' = 'true'));
