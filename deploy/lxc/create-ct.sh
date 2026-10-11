#!/bin/bash
# Run on the Proxmox VE host: creates an unprivileged Debian 13 (trixie) LXC
# container for filebrowser and bind-mounts the host directories into it.
#
#   ./create-ct.sh <ctid> <files-dir> <data-dir> <cache-dir> [storage] [bridge]
#
#   files-dir  the directory to browse (mounted at /srv/share)
#   data-dir   secret.key (mounted at /var/lib/filebrowser)
#   cache-dir  thumbnails and remuxed media (mounted at /var/cache/filebrowser)
#
# Inside the container uid/gid 990 is "filebrowser"; on the host that is
# 100990, so the three directories are chowned to it. PostgreSQL lives on the
# container's root disk (covered by vzdump backups and snapshots).
set -euo pipefail
CTID=${1:?ctid}
FILES=${2:?files dir}
DATA=${3:?data dir}
CACHE=${4:?cache dir}
STORAGE=${5:-local-lvm}
BRIDGE=${6:-vmbr0}
HOST_UID=100990

command -v pct >/dev/null || { echo "run this on a Proxmox VE host" >&2; exit 1; }

pveam update >/dev/null
TEMPLATE=$(pveam available --section system | awk '$2 ~ /^debian-13-standard_/ {print $2}' | sort -V | tail -1)
[ -n "$TEMPLATE" ] || { echo "no debian-13-standard template available" >&2; exit 1; }
pveam list local | grep -q "$TEMPLATE" || pveam download local "$TEMPLATE"

for d in "$FILES" "$DATA" "$CACHE"; do
	install -d "$d"
done
chown "$HOST_UID:$HOST_UID" "$DATA" "$CACHE"
chmod 0700 "$DATA"
echo "Make $FILES writable for the container: chown -R $HOST_UID:$HOST_UID $FILES (or an ACL)."

# nesting=1: unprivileged user namespaces for the ffmpeg sandbox (bubblewrap)
# and the systemd unit's own sandboxing.
pct create "$CTID" "local:vztmpl/$TEMPLATE" \
	--hostname filebrowser --unprivileged 1 --features nesting=1 \
	--cores 4 --memory 4096 --swap 1024 --rootfs "$STORAGE:16" \
	--net0 "name=eth0,bridge=$BRIDGE,ip=dhcp" --onboot 1 \
	--mp0 "$FILES,mp=/srv/share" \
	--mp1 "$DATA,mp=/var/lib/filebrowser" \
	--mp2 "$CACHE,mp=/var/cache/filebrowser"
pct start "$CTID"
cat <<EOF
Container $CTID is running. Next:
  pct push $CTID deploy.tar.gz /root/deploy.tar.gz       # from the release
  pct enter $CTID
  tar -xzf deploy.tar.gz && GITHUB_TOKEN=<read-only token> \\
    FB_APP_ORIGIN=https://files.example.com FB_SHARE_ORIGIN=https://share.example.com \\
    bash deploy/lxc/install.sh
EOF
