# filebrowser

A self-hosted file browser and sharer: Go backend, SvelteKit SPA embedded in a
single binary, PostgreSQL for metadata. See [PLAN.md](PLAN.md) for the design
and [deploy/README.md](deploy/README.md) for deployment.

Supported platforms:

| Target | Base | ffmpeg sandbox | Updates |
|---|---|---|---|
| **Proxmox LXC** (primary) | Debian 13 "trixie" | bubblewrap | `filebrowser-update` (backup, health check, rollback) + unattended-upgrades |
| Docker | `debian:13-slim` | bubblewrap (shipped seccomp profile) | `docker compose pull && docker compose up -d` |
| Windows | Windows 10/11, Server 2019+ | AppContainer + Job Object | `update.ps1` (backup, health check, rollback) |

Licensed under the [GNU AGPL v3](LICENSE).

## Features

- Password (argon2id) + TOTP login, OIDC (PKCE, state, nonce; accounts linked by
  issuer + subject), invite links, no public sign-up.
- Browse, search, upload (resumable tus), download with ranges, zip folders,
  rename/move/delete, trash with auto-purge.
- Share links on a separate origin: expiry, password, download limits, revocation,
  upload-only "drop" links.
- Thumbnails, image/audio/video/PDF/text previews, media pipeline (direct play,
  cached remux or AAC transcode, WebVTT subtitles, "Open in VLC" signed URLs).
- Admin page: users, grants, quotas, sessions, invites, audit log.

## Access model

Paths are relative to `files_root`. Each user has **grants** (`path` → `read` or
`write`); admins can access everything. A user sees the folders leading to their
grants and everything inside them. Per-user spaces (`users/alice:write`) and a
shared library (`media:read`) are both just grants.

TOTP is mandatory for admins and for anyone with a write grant (they can upload
and create shares). Such users get a restricted session until they enroll.

## Development

Requirements: Go 1.27.2+, Node 24 LTS with pnpm (version pinned in
`web/package.json`), Docker (for PostgreSQL), ffmpeg 6.0+. Development works on
Linux, Windows and macOS; on Windows the ffmpeg sandbox is the same
AppContainer as in production.

```sh
make dev-db                      # PostgreSQL 17 on 127.0.0.1:55432
cd web && pnpm install && pnpm run build && cd ..
cat > filebrowser.toml <<'EOF'
dev = true
app_origin = "http://files.localhost:8080"
share_origin = "http://share.localhost:8080"
database_url = "postgres://filebrowser:devpass@127.0.0.1:55432/filebrowser?sslmode=disable"
files_root = "files"
EOF
mkdir -p files
go run ./cmd/filebrowser user add admin -admin
go run ./cmd/filebrowser serve   # open http://files.localhost:8080
```

`*.localhost` resolves to loopback in modern browsers, which gives the two
origins without editing hosts files. For frontend work, `pnpm run dev` in `web/`
proxies `/api` and `/_upload` to the Go server (share pages need the real share origin).

Tests need a PostgreSQL server where the user may create databases (each test
gets a fresh database):

```sh
export FB_TEST_DATABASE_URL="postgres://filebrowser:devpass@127.0.0.1:55432/filebrowser?sslmode=disable"
go test ./...
```

Without `FB_TEST_DATABASE_URL` the database-backed tests are skipped.

## Layout

```
cmd/filebrowser/     CLI: serve, migrate, user ..., health, version, service (Windows)
internal/auth/       passwords, TOTP, keys, sessions, rate limiting, OIDC
internal/server/     routes (deny by default), middleware, handlers, static SPA
internal/files/      os.Root confinement, access checks, ops, trash, zip, search
internal/uploads/    tus integration (app uploads and drop links)
internal/shares/     share tokens, unlock cookies, signed links
internal/media/      sandboxed ffmpeg runner, probe, plans, jobs, thumbnails, subtitles
internal/sandbox/    Windows AppContainer launcher for ffmpeg
internal/store/      PostgreSQL (pgx) and embedded migrations
internal/audit/      audit log
web/                 SvelteKit 3 SPA (adapter-static, pnpm), embedded from web/build
deploy/              LXC (install/update), Docker (compose, seccomp), Windows (service, update)
```
