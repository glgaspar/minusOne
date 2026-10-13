# minusOne

Self-hosted karaoke maker. Paste a YouTube link, and minusOne downloads the song and splits it into separate tracks, so you can play it back *minus one*: the original without the vocals.

> **Status:** download and storage work end to end. Track splitting (`processor.SplitTracks`) is not implemented yet, so a song currently becomes **Ready** with only its original audio.

## How it works

```
browser ──htmx──▶ web ──▶ postgres (songs queue) ◀── processor ──▶ YouTube
                                                        │
                                                        ▼
                                                  rustfs (S3): songs/<id>/original.m4a, vocals.wav, ...
```

1. A logged-in user pastes a link in the catalog. The link is normalized to `https://www.youtube.com/watch?v=<id>`, so the same video can't be added twice, and the song is saved as `pending`.
2. The processor, running in the same binary, claims the oldest pending song (`FOR UPDATE SKIP LOCKED`). It downloads the audio with [yt-dlp](https://github.com/yt-dlp/yt-dlp) into a temporary directory and uploads it to object storage.
3. It splits the audio into tracks (TODO), uploads each track, and records it in the `tracks` table.
4. The song is marked `done`. A failed song is retried up to 3 times, then marked `failed`; its **Retry** button puts it back in the queue with 3 new attempts. A song interrupted by a shutdown goes back to the queue.

Songs can be deleted from the catalog, which also deletes their audio files. A song can't be deleted while it is being processed.
5. Rows still in progress poll their own status every 3 seconds, so the catalog updates without a page reload.

Audio files never go into Postgres. The database holds metadata and object keys, and the files live in S3-compatible storage ([RustFS](https://github.com/rustfs/rustfs)).

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

## Development

Requirements: Go 1.26, Docker, and on your PATH: a recent [yt-dlp](https://github.com/yt-dlp/yt-dlp) and [deno](https://deno.com) (yt-dlp uses it for YouTube's JavaScript challenges). Distribution packages of yt-dlp are usually too old for YouTube; install it with `pip install -U "yt-dlp[default]"`.

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
  catalog.go         catalog, search, add-song modal, status polling
  render.go          template rendering
  templates/         page and partial templates
  static/            css, js, htmx (vendored, no CDN)
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
