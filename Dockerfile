# syntax=docker/dockerfile:1

FROM golang:1.26.1-bookworm AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/minusone .

# Python for yt-dlp now and the track splitting later.
FROM python:3.12-slim-bookworm

# YouTube breaks older yt-dlp versions regularly. When downloads start failing,
# bump this (https://github.com/yt-dlp/yt-dlp/releases) and rebuild.
ARG YTDLP_VERSION=2026.08.19
RUN pip install --no-cache-dir "yt-dlp[default]==${YTDLP_VERSION}"

# yt-dlp needs a JavaScript runtime to solve YouTube's challenges; deno is its default.
COPY --from=denoland/deno:bin-2.5.6 /deno /usr/local/bin/deno

RUN useradd --system --uid 10001 --no-create-home minusone
COPY --from=build /out/minusone /usr/local/bin/minusone

USER minusone
ENV MINUSONE_ADDR=:8080
EXPOSE 8080
ENTRYPOINT ["minusone"]
