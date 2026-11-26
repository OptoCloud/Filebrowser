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
  live HLS for video the browser cannot play as is (repackaged, audio converted
  to AAC if needed), WebVTT subtitles, "Open in VLC" signed URLs).
- API tokens scoped to folders and capabilities, and one-request uploads for
  curl, ShareX and the like; signed webhooks on uploads and share use.
- Admin page: users, grants, quotas, sessions, invites, tokens, audit log.

## Access model

Paths are relative to `files_root`. Each user has **grants** (`path` → `read` or
`write`); admins can access everything. A user sees the folders leading to their
grants and everything inside them. Per-user spaces (`users/alice:write`) and a
shared library (`media:read`) are both just grants.

TOTP is mandatory for admins and for anyone with a write grant (they can upload
and create shares). Such users get a restricted session until they enroll.

## API tokens

For scripts and other software, a user makes API tokens under Settings. A token
works in the folders chosen for it and for what it was allowed (`read`,
`upload`, `modify`, `share`), optionally only from given addresses and until an
expiry. It is never more than its user's own access, follows that access as it
changes, and cannot sign in, reach the admin pages or change the account. It is
sent as `Authorization: Bearer fbt_...` to the same `/api` the web UI uses
(listing, download, zip, search, resumable tus uploads, rename, move, delete,
shares), and to two endpoints for clients that cannot speak tus:

```sh
# One file per request. An existing file is never overwritten: the answer names what was stored.
curl -T report.pdf -H "Authorization: Bearer $TOKEN" https://files.example.com/api/v1/files/inbox/report.pdf

# A multipart form (ShareX, iOS Shortcuts, HTML forms). With share=1 the answer
# has a "url" to the upload, for a token that may share.
curl -F file=@shot.png -H "Authorization: Bearer $TOKEN" "https://files.example.com/api/v1/upload?dir=shots&share=1"
```

A request body is as large as the proxy in front allows (100 MB behind
Cloudflare); larger files go through tus (`/api/uploads/`).

## Webhooks

Under Settings a user can register addresses the server calls when something
happens to their files: `file.uploaded`, `drop.received` (through one of their
drop links), `share.created`, `share.accessed`, optionally only below a folder.
A call is a POST of JSON:

```json
{ "id": "…", "event": "drop.received", "time": "2026-11-26T12:00:00Z", "path": "inbox/scan.pdf", "data": { "size": 48213, "share_id": 7 } }
```

with `X-Fb-Event`, `X-Fb-Delivery`, `X-Fb-Timestamp` and `X-Fb-Signature:
sha256=<hex>`, the HMAC-SHA256 of `<timestamp>.<body>` under the hook's secret
(shown once, when it is made). Check the signature, and that the timestamp is
recent, before acting on a call. A call is tried three times; a hook that fails
twenty times in a row is disabled. Hooks must use https and a public address,
except an administrator's, which may also call into the local network. Redirects
are not followed.

## Development

Requirements: Go 1.27.4+, Node 24 or 26 LTS with pnpm (version pinned in
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
