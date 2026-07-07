FROM golang:1.25 AS builder

ENV TAG="nightly"
ENV COMMIT=""

WORKDIR /build

COPY . .

RUN make build

# Download yt-dlp
RUN wget -O /usr/bin/yt-dlp https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp && \
    chmod a+rwx /usr/bin/yt-dlp

# Alpine 3.24 will go EOL on 2028-05-01
FROM alpine:3.24

WORKDIR /app

# deno is required for yt-dlp (ref: https://github.com/yt-dlp/yt-dlp/issues/14404)
# py3-mutagen lets yt-dlp embed thumbnails into MP3 files
RUN apk --no-cache add \
    ca-certificates=20260611-r0 \
    python3=3.14.5-r0 \
    py3-pip=26.1.2-r0 \
    py3-mutagen=1.47.0-r2 \
    pipx=1.14.0-r0 \
    ffmpeg=8.1.2-r0 \
    tzdata=2026b-r0 \
    gcompat=1.1.0-r4 \
    deno=2.7.4-r2

# Optional transcript/chapter helper tools (features degrade gracefully
# without them, but the image ships with the full experience)
ENV PIPX_HOME=/opt/pipx PIPX_BIN_DIR=/usr/local/bin
RUN pipx install podcast-transcript-convert==0.2.0 || echo "WARN: podcast-transcript-convert install failed, using built-in converter"; \
    pipx install podcast-chapter-tools==0.2.0 || echo "WARN: podcast-chapter-tools install failed, using built-in parser"; \
    pipx install video-to-chapters-with-transcript==0.1.0 || echo "WARN: video-to-chapters-with-transcript install failed, AI chapters unavailable"

RUN chmod 777 /usr/local/bin
COPY --from=builder /usr/bin/yt-dlp /usr/local/bin/youtube-dl
COPY --from=builder /build/bin/podsync /app/podsync
COPY --from=builder /build/html/index.html /app/html/index.html

ENTRYPOINT ["/app/podsync"]
CMD ["--no-banner"]
