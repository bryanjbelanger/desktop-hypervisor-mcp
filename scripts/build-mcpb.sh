#!/usr/bin/env bash
# Assemble the MCP Bundle (.mcpb) published alongside a release.
#
# The registry accepts one mcpb artifact per version and its package schema has
# no platform field, so a single bundle carries every (os, arch) binary and
# server/launch.sh resolves one at startup.
#
# Binaries come from the published release by default, which makes the bundle
# provably the same bits as the release assets; BIN_DIR overrides that for
# local testing.
#
# Usage: scripts/build-mcpb.sh <version> [outdir]
set -euo pipefail

version=${1:?usage: build-mcpb.sh <version> [outdir]}
version=${version#v}
outdir=${2:-dist}

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
binaries=(
	desktop-hypervisor-mcp-darwin-amd64
	desktop-hypervisor-mcp-darwin-arm64
	desktop-hypervisor-mcp-linux-amd64
	desktop-hypervisor-mcp-linux-arm64
	desktop-hypervisor-mcp-windows-amd64.exe
)

staging=$(mktemp -d)
trap 'rm -rf "$staging"' EXIT
mkdir -p "$staging/server"

if [ -n "${BIN_DIR:-}" ]; then
	# Searched rather than joined: GoReleaser's dist layout is an implementation
	# detail, and the release-named binaries can sit at its root or a subdir.
	for b in "${binaries[@]}"; do
		found=$(find "$BIN_DIR" -type f -name "$b" -print -quit)
		if [ -z "$found" ]; then
			echo "build-mcpb: $b not found under $BIN_DIR" >&2
			exit 1
		fi
		cp "$found" "$staging/server/$b"
	done
else
	gh release download "v$version" \
		--repo bryanjbelanger/desktop-hypervisor-mcp \
		--pattern 'desktop-hypervisor-mcp-*' \
		--dir "$staging/server"
fi

for b in "${binaries[@]}"; do
	if [ ! -s "$staging/server/$b" ]; then
		echo "build-mcpb: missing binary $b" >&2
		exit 1
	fi
	chmod +x "$staging/server/$b"
done

cp "$repo_root/mcpb/launch.sh" "$staging/server/launch.sh"
chmod +x "$staging/server/launch.sh"
sed "s/__VERSION__/$version/" "$repo_root/mcpb/manifest.json" > "$staging/manifest.json"
if grep -q "__VERSION__" "$staging/manifest.json"; then
	echo "build-mcpb: version substitution failed" >&2
	exit 1
fi

mkdir -p "$outdir"
bundle="$outdir/desktop-hypervisor-mcp-$version.mcpb"

# zip(1) is not guaranteed on a build runner, and the executable bits have to
# survive into the archive for launch.sh and the binaries to run after install.
python3 - "$staging" "$bundle" <<'PY'
import os, sys, zipfile

staging, bundle = sys.argv[1], sys.argv[2]
with zipfile.ZipFile(bundle, "w", zipfile.ZIP_DEFLATED) as z:
    for root, dirs, files in os.walk(staging):
        dirs.sort()
        for name in sorted(files):
            path = os.path.join(root, name)
            arcname = os.path.relpath(path, staging)
            info = zipfile.ZipInfo(arcname, date_time=(1980, 1, 1, 0, 0, 0))
            info.compress_type = zipfile.ZIP_DEFLATED
            info.external_attr = (os.stat(path).st_mode & 0xFFFF) << 16
            with open(path, "rb") as f:
                z.writestr(info, f.read())
PY

sha=$(python3 -c 'import hashlib,sys;print(hashlib.sha256(open(sys.argv[1],"rb").read()).hexdigest())' "$bundle")

echo "$bundle"
echo "$sha"
