#!/bin/sh
# The bundle carries one binary per (os, arch); MCPB's platform_overrides key
# on OS alone, so the architecture is resolved here at launch.
set -e

dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

case $(uname -s) in
	Darwin) os=darwin ;;
	Linux) os=linux ;;
	*) echo "desktop-hypervisor-mcp: unsupported OS $(uname -s)" >&2; exit 1 ;;
esac

case $(uname -m) in
	x86_64 | amd64) arch=amd64 ;;
	arm64 | aarch64) arch=arm64 ;;
	*) echo "desktop-hypervisor-mcp: unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac

bin="$dir/desktop-hypervisor-mcp-$os-$arch"
if [ ! -x "$bin" ]; then
	echo "desktop-hypervisor-mcp: bundle has no binary for $os/$arch" >&2
	exit 1
fi

exec "$bin" "$@"
