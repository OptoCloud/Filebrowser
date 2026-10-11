# Filebrowser: Project Plan

A self-hosted, publicly exposed file browser and sharer for pve01. Go backend, SvelteKit frontend, single binary.

## Goals

- Browse, upload, download and manage files on the NAS from anywhere.
- Share files and folders via links (with expiry, password, upload-only option).
- Stream audio and video in the browser, including FLAC and MKV.
- Thumbnails and gallery view for images and video.
- Login with username + password or OIDC, built into the app and publicly reachable.
- Secure enough to expose to the internet.

## Non-goals (v1)

- WebDAV, SFTP, FTP, SMB or any protocol other than HTTP(S).
- Sync clients, file versioning (ZFS snapshots cover this), collaborative editing.
- In-browser rendering of HTML, SVG or other active content.
- Live on-the-fly video transcoding (see Media pipeline, phase 2).
- Public self-registration.

## Architecture

```
internet -> Cloudflare Tunnel -> Go binary (unprivileged LXC on pve01)
                                   |- files.<domain>  embedded SvelteKit SPA + /api (session required)
                                   |- share.<domain>  /s/<token> (public, download/upload-only)
                                 PostgreSQL: users, sessions, credentials, shares, media cache index, audit log
                                 ffmpeg / ffprobe: sandboxed subprocesses
```

- **Backend:** Go (1.24+ for `os.Root`).
- **Frontend:** SvelteKit with `adapter-static` (SPA), embedded into the binary via `//go:embed`. No Node server in production.
- **Database:** PostgreSQL (`jackc/pgx/v5` with `pgxpool`, pure Go, no cgo). Schema migrations embedded in the binary.
- **Two origins:** the app origin holds the session cookie; the share origin serves user content. Uploaded HTML/SVG can never run with an authenticated session.
- **Filesystem is the source of truth.** The file tree is not mirrored in the DB, so files added via SMB or directly on the NAS appear immediately. The DB stores only metadata caches keyed by `path + size + mtime`.
- **Ingress:** Cloudflare Tunnel (no open router ports, IP hidden, TLS, DDoS protection, coarse WAF/rate limits). Auth is still fully in-app.

## Features

### v1 (MVP)

- Login: username + password (argon2id), TOTP 2FA, OIDC.
- Browse folders, list/grid views, sort, filename search.
- Upload: resumable chunked uploads via tus (`tusd` + Uppy or `tus-js-client`). Required because Cloudflare caps request bodies at 100 MB.
- Download: single files with range support (`http.ServeContent`), folders as streamed zip.
- File operations: create folder, rename, move, delete to trash, multi-select.
- Trash with auto-purge after N days.
- Share links: file or folder, expiry, optional password, revocable, optional download limit, upload-only "drop" links.
- Users: admin/user roles, per-user root folder and/or folder grants (read / read-write).
- Quotas: ZFS dataset quota; optional per-user quota in the app.
- Previews: images, audio, video, PDF (sandboxed iframe on the share origin).
- Thumbnails: lazy generation, cached on disk.
- Media streaming: direct play, live HLS remux / audio transcode (see Media pipeline).
- "Open in VLC/mpv" fallback via short-lived signed URL.
- Admin page: manage users, revoke sessions, view audit log.
- Mobile-friendly UI.

### v2 (later, if needed)

- Passkeys / WebAuthn.
- Live on-the-fly transcoding with hardware acceleration and seek handling.
- Subtitle burn-in for image-based subtitles (PGS/VobSub).
- Indexed search, inotify-based change detection.
- Per-share download statistics.

## Security requirements

### Request handling

- **Deny by default:** one middleware requires a valid session on every route. Explicit allowlist only: `/login`, OIDC callback, static assets, share origin routes.
- **Path confinement:** all file access goes through `os.Root` scoped to the user's allowed root. No string-based path checks. Symlinks cannot escape.
- Response headers: strict CSP, `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, HSTS, `Referrer-Policy: no-referrer`.
- User files served with `Content-Disposition: attachment` unless a known-safe preview type (image, audio, video, PDF on the share origin).
- Request body limits on all non-upload endpoints.

### Authentication

- argon2id password hashing with standard library parameters.
- Rate limiting per IP and per account, exponential backoff, alerts on bursts. No hard lockout.
- No user enumeration: identical errors and similar timing for unknown users (hash a dummy password).
- TOTP 2FA, mandatory for admins and for anyone who can upload or create shares.
- No email password reset in v1. Admins reset via CLI on the server.
- No public signup. Users are created by an admin or via expiring invite links.

### OIDC

- `coreos/go-oidc` + `golang.org/x/oauth2`, with PKCE, `state` and `nonce`.
- Accounts linked by `iss + sub`, never auto-linked by email.
- Allowed issuers and (optionally) allowed users/groups configured explicitly.

### Sessions and CSRF

- Random 256-bit session IDs stored server-side (hashed).
- Cookie: `__Host-session; HttpOnly; Secure; SameSite=Strict; Path=/`.
- Rotate the session ID on login and privilege change. Idle and absolute timeouts. "Log out all sessions."
- CSRF: SameSite=Strict plus `Origin` check (or CSRF token) on every state-changing request.

### Share links

- 128+ bit random tokens, stored hashed. Mandatory expiry (configurable max).
- Scoped to exactly one file or folder via `os.Root`.
- Rate-limited per IP to prevent token brute force.
- Media on share links: direct play only, no transcoding.

### Untrusted media processing

- ffmpeg/ffprobe always run as subprocesses, never linked in-process.
- Sandbox: read-only access to the input only (passed as a file descriptor/handle), no network, CPU/memory/time limits: bubblewrap + prlimit on Linux, AppContainer + Job Object on Windows. Only whitelisted demuxers and decoders are used.
- Pure-Go decoders for JPEG/PNG/GIF thumbnails; ffmpeg only for video frames and formats like HEIC.
- Concurrency caps for media jobs, per user and globally.

### Audit and assurance

- Audit log: logins, failures, OIDC links, share creation/revocation, share access, admin actions.
- Tests: every route without a session returns 401; traversal attempts (`..`, `%2e%2e`, symlinks, absolute paths) fail.
- CI: `go vet`, `gosec`, `govulncheck`, `semgrep`, `npm audit`; Renovate/Dependabot.
- OWASP ZAP scan against a staging instance before public exposure.

## Media pipeline

Uses the distribution's ffmpeg (Debian 13: 7.1, security-supported; jellyfin-ffmpeg optional) as a sandboxed subprocess; ffmpeg 6.0+ is required for the fd: input protocol.

1. **Probe:** `ffprobe` each media file once; cache codecs, tracks and duration in PostgreSQL.
2. **Client capabilities:** the frontend reports what it can play (`MediaSource.isTypeSupported`, `canPlayType`).
3. **Choose the cheapest path:**
   | Case | Action |
   |---|---|
   | Browser supports container and codecs (FLAC, MP4, WebM, some MKV) | Direct play with range requests |
   | MKV with supported video and audio codecs (or a non-first audio track) | Live HLS: fragmented MP4, video and audio copied (audio-only files: cached remux) |
   | Unsupported audio (AC3, DTS, TrueHD) | Live HLS: copy video, transcode audio to stereo AAC while playing (audio-only files: cached AAC transcode) |
   | Text subtitles (SRT, ASS) | Convert to WebVTT, cached |
   | Unsupported video (HEVC on non-Safari, old codecs) | v1: "Open in VLC" fallback. v2: live transcode |
4. **Live HLS:** segments are planned from the video keyframes (Matroska cues, else an ffprobe packet scan) and made on request by one ffmpeg run per stream, which writes fragmented MP4 with source timestamps to a pipe; a seek restarts the run there. Segments are kept for the session (the hourly cleanup deletes them once unused for 30 minutes), and a run stops 2 minutes ahead of playback.
5. **Cache:** remuxed/transcoded files stored in a cache dataset with LRU eviction and a size cap. Generated on first play (with progress UI) or by a background job. After that, playback is plain direct play with free seeking.
6. **External player:** short-lived HMAC-signed URL for VLC/mpv, scoped to one file.

## Data model (PostgreSQL)

- `users`: id, username, password_hash, role, quota_bytes, totp_secret (encrypted), created_at, disabled (a per-user root folder is a `write` grant on e.g. `users/alice`)
- `oidc_identities`: user_id, issuer, subject, created_at
- `sessions`: id_hash, user_id, created_at, last_seen_at, expires_at, ip, user_agent
- `grants`: user_id, path, permission (read / read-write)
- `shares`: id, token_hash, owner_id, path, kind (download / upload-only), password_hash, expires_at, max_downloads, download_count, revoked_at
- `invites`: token_hash, role, root_path, expires_at, used_at
- `trash`: id, user_id, original_path, trash_path, is_dir, size, trashed_at
- `media_probe`: path, size, mtime, probe_json
- `media_cache`: source_path, source_size, source_mtime, variant, cache_path, bytes, last_used_at
- `thumbnails`: path, size, mtime, thumb_path
- `audit_log`: id, ts, user_id, ip, action, target, details_json

## API sketch

App origin (session required unless noted):

- `POST /api/auth/login` (public), `POST /api/auth/logout`, `GET /api/auth/oidc/start` (public), `GET /api/auth/oidc/callback` (public), `POST /api/auth/totp/*`
- `GET /api/me`
- `GET /api/files?path=` list, `GET /api/files/download?path=`, `GET /api/files/zip?paths=`
- `POST /api/files/mkdir`, `POST /api/files/rename`, `POST /api/files/move`, `POST /api/files/delete`
- `/api/uploads/*` tus endpoint
- `GET /api/search?q=`
- `GET /api/thumb?path=`
- `GET /api/media/info?path=`, `POST /api/media/prepare`, `GET /api/media/stream?path=&variant=`, `POST /api/media/external-url`
- `GET/POST/DELETE /api/shares`
- `GET /api/trash`, `POST /api/trash/restore`, `POST /api/trash/purge`
- `/api/admin/*`: users, sessions, invites, audit log

Share origin (public):

- `GET /s/{token}`: share page, `GET /s/{token}/file?path=`, `POST /s/{token}/unlock` (password), `/s/{token}/upload` (tus, upload-only shares)

## Project layout

```
cmd/filebrowser/        main, CLI (serve, user add, user reset-password, migrate)
internal/auth/          passwords, totp, oidc, sessions, rate limiting
internal/http/          router, middleware (auth, csrf, headers, logging)
internal/files/         os.Root wrappers, listing, ops, zip, trash
internal/uploads/       tus integration
internal/shares/        tokens, share handlers
internal/media/         probe, thumbnails, remux/transcode jobs, sandbox runner
internal/store/         PostgreSQL access (pgx), migrations
internal/audit/
web/                    SvelteKit app (adapter-static), built into web/build and embedded
deploy/                 LXC install/update scripts, systemd units, Docker compose, Windows service scripts
```

## Deployment (pve01)

Targets: Proxmox LXC (primary), Docker and Windows; see deploy/README.md. All three run the same binary, sandbox ffmpeg (bubblewrap on Linux, AppContainer + Job Object on Windows) and update with verification, a database backup, a health check and automatic rollback.

- Unprivileged LXC, Debian 13 "trixie" (current stable, supported to 2030), running the binary as a dedicated user via systemd (hardened unit: `ProtectSystem=strict`, `NoNewPrivileges`, etc.). Updates via `filebrowser-update` and unattended-upgrades.
- Bind-mount only the share dataset(s), read-only where possible. Separate datasets for app data, thumbnail cache and media cache.
- ZFS quotas on upload and cache datasets. Regular snapshots of data datasets.
- `cloudflared` in its own LXC or the same one, routing `files.<domain>` and `share.<domain>`.
- GPU passthrough to the LXC only if v2 live transcoding is needed.
- PostgreSQL in its own LXC (or the same one), listening only on localhost / the internal bridge, dedicated role with least privilege.
- Backups: PostgreSQL via `pg_dump` (or WAL archiving with pgBackRest) and config.

## Milestones

1. **Skeleton:** Go module, router, PostgreSQL + migrations, SvelteKit with adapter-static embedded, CI with linters.
2. **Auth:** password login, sessions, CSRF, rate limiting, CLI user management, deny-by-default middleware, auth tests.
3. **Files:** `os.Root` listing, download with ranges, file ops, trash, traversal tests.
4. **Uploads:** tus, quotas.
5. **Shares:** share origin, tokens, expiry/password, upload-only links.
6. **TOTP and OIDC.**
7. **Thumbnails and previews:** sandbox runner, pure-Go images, ffmpeg video frames.
8. **Media v1:** probe, direct play, cached remux/audio transcode, WebVTT, VLC fallback.
9. **Admin and audit log.**
10. **Hardening and launch:** ZAP scan, review, deploy behind Cloudflare Tunnel.

## Open questions

1. User model: separate spaces per user, a shared library with per-folder permissions, or both?
2. OIDC provider: Google/Microsoft directly, or self-hosted (Authentik, Pocket ID)?
3. pve01 hardware: CPU model and any GPU (determines whether v2 live transcoding is realistic)?
4. Library profile: run an ffprobe scan to count files per media bucket (direct / remux / audio transcode / full transcode).
5. Domain names for the app and share origins.
6. Are upload-only "drop" links wanted in v1?

## Deferred

- **Mandatory release signatures** (deferred 2026-10-11). Releases are unsigned today: the `RELEASE_SSH_KEY` secret is not set and v0.1.0 has no `SHA256SUMS.sig`, so the updaters (`deploy/lxc/filebrowser-update`, `deploy/windows/update.ps1`) verify checksums from the same release, which proves integrity but not authenticity. To do: create a dedicated ed25519 release key, store it as `RELEASE_SSH_KEY`, commit the public key as `deploy/release_signers`, make `release.yml` fail without the secret, have `install.sh`/`install.ps1` install the signers file, and make the updaters refuse releases without a valid signature.
