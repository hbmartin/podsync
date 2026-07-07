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
RUN apk --no-cache add ca-certificates python3 py3-pip py3-mutagen pipx ffmpeg tzdata libc6-compat deno

# Optional transcript/chapter helper tools (features degrade gracefully
# without them, but the image ships with the full experience)
ENV PIPX_HOME=/opt/pipx PIPX_BIN_DIR=/usr/local/bin
RUN pipx install podcast-transcript-convert || echo "WARN: podcast-transcript-convert install failed, using built-in converter"; \
    pipx install podcast-chapter-tools || echo "WARN: podcast-chapter-tools install failed, using built-in parser"; \
    pipx install video-to-chapters-with-transcript || echo "WARN: video-to-chapters-with-transcript install failed, AI chapters unavailable"

RUN chmod 777 /usr/local/bin
COPY --from=builder /usr/bin/yt-dlp /usr/local/bin/youtube-dl
COPY --from=builder /build/bin/podsync /app/podsync
COPY --from=builder /build/html/index.html /app/html/index.html

ENTRYPOINT ["/app/podsync"]
CMD ["--no-banner"]
