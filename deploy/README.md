# Deploying filebrowser

Three supported targets. The LXC container is the primary one; Docker and
Windows run the same binary with the same security model (ffmpeg always runs
in a sandbox; the server refuses to start if the sandbox does not work).

| | Proxmox LXC | Docker | Windows |
|---|---|---|---|
| OS | Debian 13 "trixie" | `debian:13-slim` image | Windows 10/11, Server 2019+ |
| PostgreSQL | 17 (Debian), peer auth over the local socket | `postgres:17-trixie` | 17 (EDB installer) |
| ffmpeg sandbox | bubblewrap | bubblewrap (seccomp profile in `docker/`) | AppContainer + Job Object |
| Process manager | systemd (hardened unit) | Docker (built-in init reaps sandbox processes) | Windows service, virtual account |
| App updates | `filebrowser-update` | `docker compose pull` | `update.ps1` |
| OS updates | unattended-upgrades | rebuild/pull the image | Windows Update |

Version policy: Debian 13 is the current Debian stable, supported until mid-2030
(Debian LTS included). PostgreSQL 17 is Debian 13's version. The frontend is
built with Node 24 LTS (build time only; Node is not needed at runtime).

## Releases

Pushing a tag `vX.Y.Z` runs `.github/workflows/release.yml`, which publishes:

- `filebrowser-linux-amd64`, `filebrowser-linux-arm64`, `filebrowser-windows-amd64.exe`
- `deploy.tar.gz` (this directory) and `SHA256SUMS`
- `SHA256SUMS.sig` when the `RELEASE_SSH_KEY` secret holds an SSH signing key
- `ghcr.io/optocloud/filebrowser:vX.Y.Z` and `:latest` (amd64 and arm64)

The updaters verify `SHA256SUMS`. To also require the signature, put the public
key in `release_signers` next to the config
(`/etc/filebrowser/release_signers` or `C:\ProgramData\filebrowser\release_signers`):

```
filebrowser-release namespaces="filebrowser-release" ssh-ed25519 AAAA...
```

While the repository is private, the updaters need a read-only GitHub token
(fine-grained, *Contents: read* on this repository). Docker hosts need
`docker login ghcr.io` with a token that has `read:packages`.

## 1. Proxmox LXC (primary)

Unprivileged Debian 13 container, binary under systemd, PostgreSQL in the same
container on its local socket (no database password), Cloudflare Tunnel in front.

### Create the container (on the Proxmox host)

```sh
tar -xzf deploy.tar.gz
bash deploy/lxc/create-ct.sh <ctid> /tank/share /tank/fb-data /tank/fb-cache [storage] [bridge]
```

The script downloads the newest `debian-13-standard` template and creates an
unprivileged container with `nesting=1` (bubblewrap and the systemd sandboxing
need unprivileged user namespaces). The three host directories are mounted at
`/srv/share`, `/var/lib/filebrowser` and `/var/cache/filebrowser`. Inside the
container the service user is uid/gid 990, which the host sees as 100990, so
those directories must be owned by (or ACL-writable for) 100990. PostgreSQL
lives on the container's root disk, which vzdump backups and snapshots cover.

### Install (inside the container)

```sh
pct push <ctid> deploy.tar.gz /root/deploy.tar.gz && pct enter <ctid>
tar -xzf deploy.tar.gz
GITHUB_TOKEN=<read-only token> \
FB_APP_ORIGIN=https://files.example.com FB_SHARE_ORIGIN=https://share.example.com \
  bash deploy/lxc/install.sh
```

`install.sh` is idempotent. It:

- installs PostgreSQL, bubblewrap, ffmpeg (Debian's; `FFMPEG=jellyfin` uses the
  Jellyfin repository instead) and unattended-upgrades;
- creates the `filebrowser` user, the database (peer authentication) and
  `/etc/filebrowser/{filebrowser.toml,env,update.env}`;
- installs the latest release with `filebrowser-update`, the hardened systemd
  unit and a daily update timer, then starts the service and checks its health.

Offline: download the release files into a directory and pass `--from DIR`.

Create the first admin (TOTP enrollment is forced at first login):

```sh
runuser -u filebrowser -- env $(cat /etc/filebrowser/env) \
  filebrowser -config /etc/filebrowser/filebrowser.toml user add admin -admin
```

### Updating

- **filebrowser:** `filebrowser-update`. It downloads and verifies the release,
  backs up the database (`/var/backups/filebrowser`, last 5 kept), installs the
  new version next to the old one (`/usr/local/lib/filebrowser/<version>`),
  restarts, waits for `filebrowser health` and **rolls back automatically** if
  the new version is unhealthy. `filebrowser-update --rollback` switches back by
  hand; `--check` only reports. The daily `filebrowser-update.timer` reports new
  releases in the journal, or installs them when `AUTO_UPDATE=yes` is set in
  `/etc/filebrowser/update.env`. Migrations run at startup and only add to the
  schema; a backup is taken before each update in case one has to be restored.
- **Debian packages** (ffmpeg, bubblewrap, PostgreSQL minor releases, OpenSSL,
  ...): unattended-upgrades installs security and point-release updates daily.
  ffmpeg and bubblewrap are started per job, so no restart is needed.
- **Snapshots:** for extra safety take `pct snapshot <ctid> pre-update` on the
  host before larger updates.
- **Next Debian release:** Debian 14 is expected in 2027. Upgrade in place
  (`apt full-upgrade` after switching the sources to the new codename) or
  create a new container and move the data mounts and a `pg_dump`.

### Cloudflare Tunnel

Install `cloudflared` (in this container or its own) and use `cloudflared.yml`.
Set `trusted_ip_header = "CF-Connecting-IP"` and keep `listen =
"127.0.0.1:8080"`, so the app is only reachable through the tunnel and rate
limits see real client IPs (`trusted_proxies` defaults to localhost).

Recommended Cloudflare settings: a WAF rate-limit rule on `/api/auth/login`,
`/s/*/api/unlock` and `/api/auth/invite*`; a Cache Rule that bypasses caching
for both hostnames.

## 2. Docker

```sh
cd deploy/docker
cat > .env <<'EOF'
POSTGRES_PASSWORD=<random>
FB_APP_ORIGIN=https://files.example.com
FB_SHARE_ORIGIN=https://share.example.com
FILES_DIR=/path/to/files
EOF
docker compose up -d
docker compose exec filebrowser filebrowser user add admin -admin
```

- The image is Debian 13 slim with Debian's ffmpeg and bubblewrap, runs as
  uid 990 (make `FILES_DIR` writable for it), with a read-only root filesystem
  and no capabilities.
- The ffmpeg sandbox needs unprivileged user namespaces. `seccomp-bwrap.json` is
  Docker's default seccomp profile plus `clone`/`unshare`/`mount`/`umount2`/
  `pivot_root`/`setns`; Docker's AppArmor profile forbids `mount`, hence
  `apparmor=unconfined`. Do not grant capabilities or `privileged` instead.
- On hosts that restrict unprivileged user namespaces (Ubuntu 23.10+), set
  `sysctl kernel.apparmor_restrict_unprivileged_userns=0`, or the server
  refuses to start with a media self-test error.
- filebrowser runs as PID 1 and reaps the sandbox's helper processes itself; no
  `--init` needed.
- Update: `docker compose pull && docker compose up -d` (pin a version tag
  instead of `latest` if you prefer explicit updates; back up with
  `docker compose exec postgres pg_dump -U filebrowser -Fc filebrowser > backup.dump`).

## 3. Windows

Prerequisites: PostgreSQL 17 (`winget install PostgreSQL.PostgreSQL.17`; then
run `postgres.sql` with psql), and ffmpeg 6.0+ placed under Program Files, for
example `C:\Program Files\ffmpeg\bin`.

From an elevated PowerShell 7 prompt, in the extracted `deploy\windows`:

```powershell
.\install.ps1 -AppOrigin https://files.example.com -ShareOrigin https://share.example.com `
  -FilesRoot D:\Share -DatabaseUrl 'postgres://filebrowser:<password>@localhost/filebrowser' `
  -FFmpegDir 'C:\Program Files\ffmpeg\bin' -GitHubToken <read-only token>
& 'C:\Program Files\filebrowser\filebrowser.exe' -config C:\ProgramData\filebrowser\filebrowser.toml user add admin -admin
```

- The service runs as the virtual account `NT SERVICE\filebrowser`, with access
  only to `C:\ProgramData\filebrowser\{data,cache}`, the config (read) and
  `FilesRoot`. It starts automatically and restarts on failure; logs go to the
  Event Log (Application, source "filebrowser").
- ffmpeg runs in an AppContainer without capabilities: no network, no access
  to files except the input (passed as a handle) and a per-job scratch folder,
  and inside a Job Object (memory and CPU limits, no child processes, killed
  together with the server).
- Update: `& 'C:\Program Files\filebrowser\update.ps1'` (same verification,
  database backup via pg_dump, health check and automatic rollback as on
  Linux; `-Check`, `-Rollback`, `-From DIR`). Schedule it with Task Scheduler
  if you want automatic updates.
- Put a TLS-terminating proxy or Cloudflare Tunnel in front, as on Linux.

## Before going public

- CI is green: Go tests on Debian 13 and Windows, gosec, staticcheck,
  govulncheck, semgrep, pnpm audit.
- OWASP ZAP baseline/full scan against a staging instance, authenticated with a
  test account.
- The two origins are different hosts and HSTS is served.
