#!/bin/bash
# Runs the Go tests on the deployment target (Debian 13 "trixie": its ffmpeg,
# bubblewrap and util-linux) as an unprivileged user, like the LXC service.
# Expects the repo at /src, Go at /usr/local/go and modules at /gomod.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq --no-install-recommends ca-certificates gcc libc6-dev ffmpeg bubblewrap util-linux >/dev/null
. /etc/os-release
echo "$PRETTY_NAME / $(ffmpeg -version | head -1) / $(bwrap --version)"
useradd -m tester
cp -r /src /work
chown -R tester /work
runuser -u tester -- env -i HOME=/home/tester PATH=/usr/local/go/bin:/usr/bin:/bin \
  GOTOOLCHAIN=local GOPROXY=off GOMODCACHE=/gomod GOFLAGS=-buildvcs=false CGO_ENABLED=1 \
  FB_TEST_DATABASE_URL="$FB_TEST_DATABASE_URL" \
  bash -c 'cd /work && go test -race -count=1 ./...'
