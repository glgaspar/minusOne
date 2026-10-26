# syntax=docker/dockerfile:1

FROM golang:1.26.1-bookworm AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/minusone .

# The track splitter's packages, built where there's a compiler (diffq has no
# wheel for this Python), so the runtime image doesn't need one.
FROM python:3.12-bookworm AS separator
COPY separator-requirements.txt .
RUN --mount=type=cache,target=/root/.cache/pip \
    pip wheel -r separator-requirements.txt -w /wheels

# Python for yt-dlp and the track splitter.
FROM python:3.12-slim-bookworm

# ffmpeg decodes and encodes the audio around the separation.
RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg \
 && rm -rf /var/lib/apt/lists/*

RUN --mount=type=bind,from=separator,source=/wheels,target=/wheels \
    --mount=type=bind,source=separator-requirements.txt,target=/tmp/separator-requirements.txt \
    pip install --no-cache-dir --no-index --find-links /wheels -r /tmp/separator-requirements.txt

# YouTube breaks older yt-dlp versions regularly. When downloads start failing,
# bump this (https://github.com/yt-dlp/yt-dlp/releases) and rebuild.
ARG YTDLP_VERSION=2026.08.19
RUN pip install --no-cache-dir "yt-dlp[default]==${YTDLP_VERSION}"

# yt-dlp needs a JavaScript runtime to solve YouTube's challenges; deno is its default.
COPY --from=denoland/deno:bin-2.5.6 /deno /usr/local/bin/deno

RUN useradd --system --uid 10001 --no-create-home minusone

# The separation models (~120 MB) download on first use into a volume, so only
# once. The user has no home: caches go there too.
RUN mkdir /models && chown minusone /models
ENV MINUSONE_MODELS=/models XDG_CACHE_HOME=/models/.cache
COPY --from=build /out/minusone /usr/local/bin/minusone

USER minusone
ENV MINUSONE_ADDR=:8080
EXPOSE 8080
ENTRYPOINT ["minusone"]
