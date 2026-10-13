# syntax=docker/dockerfile:1

FROM golang:1.26.1-bookworm AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/minusone .

# Debian rather than distroless: the track splitting will need python and ffmpeg here.
FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 10001 --no-create-home minusone

COPY --from=build /out/minusone /usr/local/bin/minusone

USER minusone
ENV MINUSONE_ADDR=:8080
EXPOSE 8080
ENTRYPOINT ["minusone"]
