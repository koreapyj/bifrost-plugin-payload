#!/bin/sh
# Builds the management UI assets embedded by the ui package (ui/web/dist):
#   index.html, app.js, app.css  - React app bundled by esbuild (needs Node/npm)
#   engine.wasm.gz               - payload engine compiled with GOOS=js GOARCH=wasm, gzip -9
#   wasm_exec.js                 - Go's JS glue, copied from the active Go toolchain
#
# Usage: ui/build.sh [all|web|wasm]   (default: all)
# Environment: NPM_INSTALL=0 skips `npm ci` when node_modules already exists.
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
WEB="$ROOT/ui/web"
DIST="$WEB/dist"
what=${1:-all}

build_web() {
	if [ ! -d "$WEB/node_modules" ] || [ "${NPM_INSTALL:-1}" != 0 ]; then
		(cd "$WEB" && npm ci --no-audit --no-fund)
	fi
	(cd "$WEB" && npm run build)
}

build_wasm() {
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT
	(cd "$ROOT" && GOOS=js GOARCH=wasm go build -trimpath -ldflags="-s -w" -o "$tmp/engine.wasm" ./ui/wasm)
	# Only the compressed file goes into dist: everything in dist is embedded.
	gzip -9 -n -c "$tmp/engine.wasm" >"$DIST/engine.wasm.gz"
	rm -f "$DIST/engine.wasm"
	goroot=$(go env GOROOT)
	for f in "$goroot/lib/wasm/wasm_exec.js" "$goroot/misc/wasm/wasm_exec.js"; do
		if [ -f "$f" ]; then
			cp "$f" "$DIST/wasm_exec.js"
			return 0
		fi
	done
	echo "wasm_exec.js not found under $goroot" >&2
	exit 1
}

mkdir -p "$DIST"
touch "$DIST/.keep"
case "$what" in
all)
	find "$DIST" -mindepth 1 ! -name .keep -exec rm -rf {} +
	build_web
	build_wasm
	;;
web) build_web ;;
wasm) build_wasm ;;
*)
	echo "usage: $0 [all|web|wasm]" >&2
	exit 2
	;;
esac
ls -l "$DIST"
