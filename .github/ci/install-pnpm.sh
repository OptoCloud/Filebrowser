#!/bin/sh
# Installs the pnpm that web/package.json pins in "packageManager"
# ("pnpm@<version>+sha512.<hex>"): the release tarball is fetched and checked
# against that hash before npm installs it, so the tool that builds what is
# published is the one that was reviewed, whatever the registry says later.
# Usage: install-pnpm.sh [path to package.json]
set -eu
spec=$(node -p "require(require('path').resolve('${1:-package.json}')).packageManager")
case "$spec" in
pnpm@*+sha512.*) ;;
*)
	echo "packageManager must be pnpm@<version>+sha512.<hex>, got: $spec" >&2
	exit 1
	;;
esac
version=${spec#pnpm@}
version=${version%%+*}
sum=${spec##*+sha512.}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
node -e '
	const [url, out] = process.argv.slice(1);
	fetch(url).then(async (r) => {
		if (!r.ok) throw new Error(url + ": " + r.status);
		require("fs").writeFileSync(out, Buffer.from(await r.arrayBuffer()));
	}).catch((e) => { console.error(e.message); process.exit(1); });
' "https://registry.npmjs.org/pnpm/-/pnpm-$version.tgz" "$tmp/pnpm.tgz"
echo "$sum  $tmp/pnpm.tgz" | sha512sum -c - >/dev/null
npm install -g --ignore-scripts "$tmp/pnpm.tgz"
pnpm --version
