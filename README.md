# minusOne

Self-hosted karaoke maker. Paste a YouTube link, and minusOne downloads the song and splits it into separate tracks, so you can play it back *minus one*: the original without the vocals.

## How it works

```
browser ──htmx──▶ web ──▶ postgres (songs queue) ◀── processor ──▶ YouTube
                                                        │
                                                        ▼
                                                  rustfs (S3): songs/<id>/original.webm, vocals.webm, ...
```

1. A logged-in user pastes a link in the catalog. The link is normalized to `https://www.youtube.com/watch?v=<id>`, so the same video can't be added twice, and the song is saved as `pending`.
2. The processor, running in the same binary, claims the oldest pending song (`FOR UPDATE SKIP LOCKED`). It downloads the audio with [yt-dlp](https://github.com/yt-dlp/yt-dlp) into a temporary directory and uploads it to object storage.
3. It splits the audio into vocals and instrumental (see [Track splitting](#track-splitting)), uploads each track, and records it in the `tracks` table.
4. The song is marked `done`. A failed song is retried up to 3 times, then marked `failed`; its **Retry** button puts it back in the queue with 3 new attempts. A song interrupted by a shutdown goes back to the queue.

Songs can be deleted from the catalog, which also deletes their audio files. A song can't be deleted while it is being processed.
5. Rows still in progress poll their own status every 3 seconds, so the catalog updates without a page reload.

Audio files never go into Postgres. The database holds metadata and object keys, and the files live in S3-compatible storage ([RustFS](https://github.com/rustfs/rustfs)).

## Playing

There is one shared play queue. Anyone can add **Ready** songs from the catalog with **+ Queue**, e.g. from their phone, while one screen (a TV or laptop) has the **Player** page open.

- The player plays the first song in the queue. When it ends, or someone skips it from any device, the song is removed and the next one starts. New songs are picked up within a few seconds.
- Browsers only allow sound after a click, so the player needs one click on **Start player** each time the page is opened.
- Only one device plays the queue: the first one to start the player owns it. Other devices that open the Player page see what it's playing (and where, or if it's paused) but make no sound. They can still add songs and remove upcoming ones, but only the owner can skip the song playing.
- Another device can **Ask to play here**. The owner gets a prompt and can pass the player on, and the song continues on the new device from the same spot. Unanswered requests expire after 30 seconds. The owner can also click **Stop here** to give the player up.
- The owner keeps the player by polling. If its tab is closed or the computer sleeps, the player is free again after 15 seconds and the next device to start it takes over, picking up the song where it stopped.
- The song's YouTube video plays muted. The sound comes from the song's tracks, mixed in the browser: each track has its own volume and mute. It starts as karaoke: the **Instrumental** plays and the **Vocals** are muted. **Original** is the downloaded audio, muted once a song is split so it doesn't double the band. Volume and mute settings are remembered per track type on that screen.
- The video follows the audio and is re-seeked if it drifts. If the owner doesn't allow embedding, the thumbnail is shown instead.

Audio is served by the app (`/songs/<id>/audio/<track>`) to logged-in users, so the storage stays private.

## Practicing

**Practice** on a Ready song in the catalog plays it on your device only, outside the shared queue (the device playing the queue isn't affected). It needs the app over HTTPS, or localhost: browsers only allow the audio engine and the microphone there.

- **Loop:** drag across the waveform to pick a section, or use **Set A** / **Set B** at the playhead. A click on the waveform seeks. **Count-in** adds 1–3 seconds of clicks before playing and before every repeat.
- **Speed and key:** slow down to 50% (or speed up to 120%) without changing the pitch, and move the key up to 6 semitones either way. All tracks are stretched with the same cuts, so they stay together. The video follows at the closest speed YouTube offers.
- **Presets:** Full, Karaoke (no vocals) and Vocals only, plus Solo on every track. They need split tracks.
- **Sections:** **Save loop…** keeps the current loop under a name, to come back to it with one click. Sections are per user.
- **Record:** sings over the song (use headphones). The take lines up with the song, gets its own row in the mixer and an **Offset** slider for fine-tuning, and can be downloaded as WAV. It plays only at the speed and key it was recorded at, and it never leaves your browser.
- Keyboard: Space, ←/→, `[`/`]`, `L`, `Esc`, `-`/`=`, `,`/`.`, `R`; `?` lists them.

Loop, count-in, speed and key are remembered per song on that device, and the mixer has its own settings, apart from the queue player's.

## Track splitting

The processor splits songs with [audio-separator](https://github.com/nomadkaraoke/python-audio-separator), on the CPU, in two passes. The original is decoded with ffmpeg, separated by a model, and every track is stored as Opus in WebM.

| Pass | When | Model (default) | Tracks |
|---|---|---|---|
| Karaoke | right after the download; the song is **Ready** after it | `UVR_MDXNET_KARA_2.onnx` | `vocals`, `instrumental` |
| Learning | when no song waits for its karaoke pass | `htdemucs_6s.yaml` (Demucs) | `drums`, `bass`, `guitar`, `piano`, `other` |

- The karaoke model removes the lead vocal and keeps backing vocals in the instrumental, like most karaoke tracks. The learning model's own vocals are dropped: the karaoke ones are cleaner.
- A learning pass gives way: if a song is added while one runs, it stops and is requeued, and the new song's karaoke pass goes first. The catalog shows when a song is being prepared for practice, and **Retry** if that failed.
- It takes time: on an 8-thread laptop CPU, the karaoke pass takes about 1.2–1.6× the song's length and up to 3.5 GB of memory; the learning pass about 2–3× and up to 4.5 GB. Both run under `nice`, so the web app stays responsive.
- Models download on first use (about 120 MB, plus 1.2 GB for timing lyrics) into the `models` volume.
- A pass that runs over 40 minutes, or gets killed (usually out of memory), fails like any other error.
- Songs that were Ready before splitting existed get their tracks through the learning pass, karaoke tracks first, and stay playable meanwhile.
- To try other models, set `MINUSONE_KARAOKE_MODEL` / `MINUSONE_LEARNING_MODEL` to ones from `audio-separator --list_models`.

The models are trained by the [Ultimate Vocal Remover](https://github.com/Anjok07/ultimatevocalremovergui) community.

## Lyrics

Once a song's karaoke pass is done, the processor looks its lyrics up on [LRCLIB](https://lrclib.net), a free, community-made database of lyrics timed line by line. The player shows them over the video (the **Lyrics** button turns them off on that screen).

- The search terms come from the video's title and channel: "Artist - Title (Official Video)" becomes artist and title, and "(Ao Vivo)", "[HD]", "ft. …" and the like are dropped.
- Of the entries found, it takes the one closest in length to the song (within 15 s), with at least a few timed lines. Entries can be wrong or for another version of the song; the timing of an entry is that version's.
- If LRCLIB has nothing, or can't be reached, the song is Ready anyway, and the learning pass tries again.
- The learning pass then times the lyrics **word by word** against the song's own vocals track, with [ctc-forced-aligner](https://github.com/deskpai/ctc_forced_aligner) (an ONNX build of Meta's multilingual MMS alignment model, licensed CC-BY-NC 4.0: fine for a home server, not for anything commercial): the player lights the words up as they're sung. It uses the words, not LRCLIB's times, so lyrics timed for another version of the song (a longer intro) still line up. It takes about 0.6× the song's length and up to 4.5 GB of memory; the model (1.2 GB) downloads on first use.
- If the words don't fit the vocals (another song's lyrics, say), the line timing stays as it was.
- The practice page lists the lyrics: the line being sung is highlighted, a click on a line plays from there, and ⟲ loops it.

Lyrics are copyrighted: they're shown here for the people using your server, like the songs themselves.

## Running with Docker

Requirements: Docker with Compose v2.

```sh
cp .env.example .env
# set POSTGRES_PASSWORD and S3_SECRET_KEY in .env
docker compose up -d --build
```

Open http://localhost:8080 and create an account.

Compose starts three services:

| Service | Image | Ports |
|---|---|---|
| `app` | built from `Dockerfile` | `${MINUSONE_PORT:-8080}` |
| `db` | `postgres:17` | `127.0.0.1:5432` |
| `s3` | `rustfs/rustfs:1.0.1` | `127.0.0.1:9000` (S3 API), `127.0.0.1:9001` (web console) |

The app waits for `db` and `s3` to report healthy before starting. Postgres and RustFS only listen on localhost.

Database migrations run automatically on every start.

## Configuration

### `.env` (used by docker compose)

| Variable | Default | Description |
|---|---|---|
| `POSTGRES_PASSWORD` | **required** | Database password |
| `POSTGRES_USER` | `minusone` | Database user |
| `POSTGRES_DB` | `minusone` | Database name |
| `S3_SECRET_KEY` | **required** | RustFS secret key (password) |
| `S3_ACCESS_KEY` | `minusone` | RustFS access key (username) |
| `S3_BUCKET` | `minusone` | Bucket for audio files; created on startup if missing |
| `MINUSONE_PORT` | `8080` | Port published on the host |
| `MINUSONE_REGISTRATION` | `open` | `open` or `closed`; whether anyone can create an account |

Wrap values in single quotes so characters like `$` and `&` are taken literally.

Postgres only reads `POSTGRES_USER`, `POSTGRES_PASSWORD` and `POSTGRES_DB` when it creates its volume. To change the password later, also change it in the database:

```sh
docker compose exec db sh -c 'echo "ALTER USER \"$POSTGRES_USER\" PASSWORD :'"'"'pw'"'"';" | psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v pw="$POSTGRES_PASSWORD"'
```

### App environment (`MINUSONE_*`)

The binary itself reads these. Compose sets them from `.env`; set them yourself when running outside Docker. The defaults point to localhost.

| Variable | Default |
|---|---|
| `MINUSONE_ADDR` | `:8080` |
| `MINUSONE_REGISTRATION` | `open` |
| `MINUSONE_DB_HOST` | `localhost` |
| `MINUSONE_DB_PORT` | `5432` |
| `MINUSONE_DB_USER` | `minusone` |
| `MINUSONE_DB_PASSWORD` | `minusone` |
| `MINUSONE_DB_NAME` | `minusone` |
| `MINUSONE_DB_SSLMODE` | `disable` |
| `MINUSONE_S3_ENDPOINT` | `localhost:9000` |
| `MINUSONE_S3_ACCESS_KEY` | `minusone` |
| `MINUSONE_S3_SECRET_KEY` | `minusone-secret` |
| `MINUSONE_S3_BUCKET` | `minusone` |
| `MINUSONE_S3_SSL` | `false` |
| `MINUSONE_KARAOKE_MODEL` | `UVR_MDXNET_KARA_2.onnx` |
| `MINUSONE_LEARNING_MODEL` | `htdemucs_6s.yaml` |

## Development

Requirements: Go 1.26, Docker, and on your PATH: a recent [yt-dlp](https://github.com/yt-dlp/yt-dlp) and [deno](https://deno.com) (yt-dlp uses it for YouTube's JavaScript challenges), ffmpeg, audio-separator, and `minusone-analyze` (`analysis/analyze.py`, linked or copied onto your PATH). Distribution packages of yt-dlp are usually too old for YouTube; install it with `pip install -U "yt-dlp[default]"`. For those two, use Python 3.12 and a C/C++ compiler (some dependencies build from source): `pip install -r requirements.txt`, in a virtualenv.

Run Postgres and RustFS in Docker and the app on the host:

```sh
docker compose up -d db s3
docker compose stop app   # frees port 8080 if the app container is running

set -a; . ./.env; set +a
MINUSONE_DB_PASSWORD="$POSTGRES_PASSWORD" MINUSONE_S3_SECRET_KEY="$S3_SECRET_KEY" go run .
```

Tests:

```sh
go test ./...
```

Templates and static files are embedded in the binary, so restart `go run .` after editing them.

## Project layout

```
main.go              config from env, starts the web server and the processor
db/                  Postgres: connection, migrations, queries (shared by web and processor)
  migrations/        SQL files, applied in name order on startup
processor/           the song work
  processor.go       queue loop: claim, process, mark done / failed / requeued
  song.go            download (yt-dlp), split (TODO), upload, save tracks
  url.go             YouTube link normalization
  storage.go         S3 client for audio files
web/                 the web app (echo + htmx)
  server.go          routes and server lifecycle
  auth.go            register, login, sessions
  catalog.go         catalog, search, add-song modal, status polling, retry, delete
  player.go          player page, play queue, audio streaming
  owner.go           which device plays the queue: lease, requests, handoff
  practice.go        practice page and saved sections
  render.go          template rendering
  templates/         page and partial templates
  static/            css, js, htmx (vendored, no CDN)
    playback.js      shared by both players: video sync, mixer UI and its saved settings
    player.js        queue player: audio mixer, polling, ownership
    practice.js      practice page: loop, waveform, presets, sections, recording
    practice-worklet.js  practice audio engine (AudioWorklet): tracks in lockstep, loop, speed/key, takes
    vendor/          soundtouch.js (soundtouchjs 0.3.0, LGPL-2.1) for speed and key
```

## When downloads start failing

YouTube changes its player regularly, and older yt-dlp versions stop working (typically `HTTP Error 403` or `Requested format is not available` in `docker compose logs app`). The image pins yt-dlp in the `Dockerfile` (`ARG YTDLP_VERSION`). To update, set it to the [latest release](https://github.com/yt-dlp/yt-dlp/releases) and rebuild:

```sh
docker compose build --build-arg YTDLP_VERSION=<version> app && docker compose up -d app
```

Songs that failed in the meantime can be put back in the queue with their **Retry** button in the catalog.

## Deploying

Before exposing minusOne outside your network:

- **Use HTTPS.** Put a reverse proxy in front of the app, e.g. Caddy or nginx. The session cookie is marked `Secure` when the proxy sends `X-Forwarded-Proto: https`.
- **Close registration.** Once everyone has an account, set `MINUSONE_REGISTRATION=closed`. With registration open, anyone who can reach the server can make it download from YouTube.
- **Back up** the `minusone_pgdata` and `minusone_s3data` volumes.
