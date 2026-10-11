# syntax=docker/dockerfile:1
# Multi-arch image on Debian 13 (trixie) slim. Build stages run natively and
# cross-compile; only the runtime stage runs for the target platform.

FROM --platform=$BUILDPLATFORM node:24-trixie-slim AS web
WORKDIR /src/web
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml web/.npmrc ./
RUN npm install -g "$(node -p "require('./package.json').packageManager")" && pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm run build

FROM --platform=$BUILDPLATFORM golang:1.27-trixie AS go
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/build web/build
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags="-s -w -X main.version=$VERSION" -o /out/filebrowser ./cmd/filebrowser

FROM debian:13-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ffmpeg bubblewrap util-linux ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && groupadd -g 990 filebrowser \
 && useradd -u 990 -g 990 -d /var/lib/filebrowser -s /usr/sbin/nologin filebrowser \
 && mkdir -p /srv/files /var/lib/filebrowser /var/cache/filebrowser /etc/filebrowser \
 && chown filebrowser:filebrowser /var/lib/filebrowser /var/cache/filebrowser
COPY --from=go /out/filebrowser /usr/local/bin/filebrowser
COPY deploy/docker/filebrowser.toml /etc/filebrowser/filebrowser.toml
USER filebrowser
ENV FB_CONFIG=/etc/filebrowser/filebrowser.toml
EXPOSE 8080
VOLUME ["/var/lib/filebrowser", "/var/cache/filebrowser"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s CMD ["filebrowser", "health"]
# filebrowser reaps orphaned sandbox processes itself when it runs as PID 1.
ENTRYPOINT ["filebrowser"]
CMD ["serve"]
