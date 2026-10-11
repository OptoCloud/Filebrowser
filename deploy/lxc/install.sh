#!/bin/bash
# Installs filebrowser in a fresh Debian 13 (trixie) LXC container, as root,
# from an extracted release deploy.tar.gz:
#
#   FB_APP_ORIGIN=https://files.example.com FB_SHARE_ORIGIN=https://share.example.com \
#     bash deploy/lxc/install.sh [--from DIR]
#
# --from DIR installs the release files in DIR (filebrowser-linux-<arch>,
# deploy.tar.gz, SHA256SUMS) instead of downloading the latest release.
# Optional environment: FB_REPO (default optocloud/filebrowser), GITHUB_TOKEN
# (read-only token for a private repository), FFMPEG=debian|jellyfin
# (default debian), FB_UID (default 990; the host sees uid 100000+FB_UID).
#
# Safe to re-run: existing configuration, database and data are kept.
set -euo pipefail

FROM=
[ "${1:-}" = --from ] && FROM=${2:?--from needs a directory}
FB_REPO=${FB_REPO:-optocloud/filebrowser}
FFMPEG=${FFMPEG:-debian}
FB_UID=${FB_UID:-990}
HERE=$(cd "$(dirname "$0")" && pwd)
ETC=/etc/filebrowser

log() { echo "install: $*" >&2; }
die() { log "error: $*"; exit 1; }
[ "$(id -u)" = 0 ] || die "run as root"

. /etc/os-release
if [ "$ID" != debian ] || [ "${VERSION_ID%%.*}" -lt 13 ]; then
	die "needs Debian 13 (trixie) or newer, found $PRETTY_NAME"
fi

log "installing packages"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq --no-install-recommends \
	postgresql bubblewrap util-linux ca-certificates curl jq openssh-client \
	unattended-upgrades apt-listchanges >/dev/null

case "$FFMPEG" in
	debian)
		apt-get install -y -qq --no-install-recommends ffmpeg >/dev/null
		FFMPEG_DIR=/usr/bin
		;;
	jellyfin)
		apt-get install -y -qq --no-install-recommends gpg >/dev/null
		install -d -m 0755 /etc/apt/keyrings
		curl -fsSL --proto '=https' https://repo.jellyfin.org/jellyfin_team.gpg.key |
			gpg --dearmor --yes -o /etc/apt/keyrings/jellyfin.gpg
		cat >/etc/apt/sources.list.d/jellyfin.sources <<-EOF
			Types: deb
			URIs: https://repo.jellyfin.org/debian
			Suites: $VERSION_CODENAME
			Components: main
			Signed-By: /etc/apt/keyrings/jellyfin.gpg
		EOF
		apt-get update -qq
		apt-get install -y -qq --no-install-recommends jellyfin-ffmpeg7 >/dev/null
		FFMPEG_DIR=/usr/lib/jellyfin-ffmpeg
		;;
	*) die "FFMPEG must be debian or jellyfin" ;;
esac

log "enabling automatic security updates"
cat >/etc/apt/apt.conf.d/20auto-upgrades <<'EOF'
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
EOF
cat >/etc/apt/apt.conf.d/52filebrowser-unattended <<'EOF'
// Debian security and point-release updates (ffmpeg, bubblewrap, PostgreSQL),
// plus the Jellyfin repository when it is used.
Unattended-Upgrade::Origins-Pattern {
	"origin=Debian,codename=${distro_codename},label=Debian";
	"origin=Debian,codename=${distro_codename}-security,label=Debian-Security";
	"origin=Jellyfin";
};
EOF

log "creating user and directories"
getent group filebrowser >/dev/null || groupadd --system -g "$FB_UID" filebrowser
id filebrowser >/dev/null 2>&1 ||
	useradd --system -u "$FB_UID" -g filebrowser -d /var/lib/filebrowser -s /usr/sbin/nologin filebrowser
install -d -m 0750 -o root -g filebrowser "$ETC"
install -d -m 0700 -o filebrowser -g filebrowser /var/lib/filebrowser
install -d -m 0750 -o filebrowser -g filebrowser /var/cache/filebrowser
[ -d /srv/share ] || install -d -m 0750 -o filebrowser -g filebrowser /srv/share
[ "$(stat -c %U /srv/share)" = filebrowser ] ||
	log "warning: /srv/share is not owned by filebrowser; on the Proxmox host run: chown -R $((100000 + FB_UID)):$((100000 + FB_UID)) <dataset>"

log "setting up PostgreSQL (local socket, peer authentication: no password)"
systemctl enable --now postgresql >/dev/null
if ! runuser -u postgres -- psql -tAc "SELECT 1 FROM pg_roles WHERE rolname = 'filebrowser'" | grep -q 1; then
	runuser -u postgres -- psql -q <<-'EOF'
		CREATE ROLE filebrowser LOGIN;
		CREATE DATABASE filebrowser OWNER filebrowser;
		REVOKE ALL ON DATABASE filebrowser FROM PUBLIC;
		\connect filebrowser
		REVOKE CREATE ON SCHEMA public FROM PUBLIC;
		GRANT ALL ON SCHEMA public TO filebrowser;
	EOF
fi
[ -f "$ETC/env" ] || install -m 0640 -g filebrowser /dev/stdin "$ETC/env" <<'EOF'
FB_DATABASE_URL=postgres:///filebrowser?host=/var/run/postgresql
EOF

[ -f "$ETC/update.env" ] || install -m 0600 /dev/stdin "$ETC/update.env" <<EOF
# Read by filebrowser-update.
FB_REPO=$FB_REPO
# Read-only GitHub token (fine-grained, "Contents: read" on the repository);
# needed while the repository is private.
GITHUB_TOKEN=${GITHUB_TOKEN:-}
# yes: the daily timer installs new releases (with backup, health check and
# rollback). no: it only reports them in the journal.
AUTO_UPDATE=no
EOF

log "installing filebrowser"
install -m 0755 "$HERE/filebrowser-update" /usr/local/sbin/filebrowser-update
if [ -n "$FROM" ]; then
	filebrowser-update --from "$FROM"
else
	filebrowser-update # latest release (settings from update.env)
fi

if [ ! -f "$ETC/filebrowser.toml" ]; then
	log "writing $ETC/filebrowser.toml"
	filebrowser config-example |
		sed -e "s#^app_origin = .*#app_origin = \"${FB_APP_ORIGIN:-https://files.example.com}\"#" \
			-e "s#^share_origin = .*#share_origin = \"${FB_SHARE_ORIGIN:-https://share.example.com}\"#" \
			-e "s#^database_url = .*#database_url = \"postgres:///filebrowser?host=/var/run/postgresql\"#" \
			-e "s#^ffmpeg = .*#ffmpeg = \"$FFMPEG_DIR/ffmpeg\"#" \
			-e "s#^ffprobe = .*#ffprobe = \"$FFMPEG_DIR/ffprobe\"#" |
		install -m 0640 -g filebrowser /dev/stdin "$ETC/filebrowser.toml"
fi

systemctl daemon-reload
systemctl enable --now filebrowser-update.timer >/dev/null
systemctl enable filebrowser >/dev/null
systemctl restart filebrowser
for _ in $(seq 1 30); do
	(set -a && . "$ETC/env" && filebrowser -config "$ETC/filebrowser.toml" health) >/dev/null 2>&1 && break
	sleep 2
done
if (set -a && . "$ETC/env" && filebrowser -config "$ETC/filebrowser.toml" health) >/dev/null 2>&1; then
	log "filebrowser $(filebrowser version) is running"
else
	journalctl -u filebrowser -n 30 --no-pager >&2 || true
	die "filebrowser did not become healthy; check $ETC/filebrowser.toml"
fi
cat >&2 <<EOF

Next steps:
  - Review $ETC/filebrowser.toml (origins, trusted_proxies, OIDC).
  - Create the first admin:
      runuser -u filebrowser -- env \$(cat $ETC/env) filebrowser -config $ETC/filebrowser.toml user add admin -admin
  - Updates: filebrowser-update (or set AUTO_UPDATE=yes in $ETC/update.env).
EOF
