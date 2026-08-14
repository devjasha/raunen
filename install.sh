#!/bin/sh
# Install raunen.
#
#   curl -fsSL https://raunen.sh | sh
#   curl -fsSL https://raw.githubusercontent.com/devjasha/raunen/main/install.sh | sh
#
# Environment:
#   RAUNEN_VERSION      a tag such as v0.1.0, or "latest" (the default)
#   RAUNEN_INSTALL_DIR  where to put the binary (default ~/.local/bin)
set -eu

REPO="devjasha/raunen"
BIN="raunen"
INSTALL_DIR="${RAUNEN_INSTALL_DIR:-$HOME/.local/bin}"
VERSION="${RAUNEN_VERSION:-latest}"

say() { printf '%s\n' "$*"; }
die() { printf 'install: %s\n' "$*" >&2; exit 1; }

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
	darwin | linux) ;;
	*) die "unsupported OS: $os. Build from source: go build -o $INSTALL_DIR/$BIN ." ;;
esac

arch=$(uname -m)
case "$arch" in
	x86_64 | amd64) arch="amd64" ;;
	arm64 | aarch64) arch="arm64" ;;
	*) die "unsupported architecture: $arch. Build from source: go build -o $INSTALL_DIR/$BIN ." ;;
esac

asset="${BIN}_${os}_${arch}.tar.gz"
if [ "$VERSION" = "latest" ]; then
	base="https://github.com/$REPO/releases/latest/download"
else
	base="https://github.com/$REPO/releases/download/$VERSION"
fi

command -v curl >/dev/null 2>&1 || die "curl is required"
command -v tar >/dev/null 2>&1 || die "tar is required"

tmp=$(mktemp -d)
# Clean up on any exit, including an interrupted download.
trap 'rm -rf "$tmp"' EXIT INT TERM

say "downloading $asset ($VERSION)"
curl -fsSL "$base/$asset" -o "$tmp/$asset" ||
	die "could not download $base/$asset — check that the release exists"

# Verify the download rather than trusting the pipe. A truncated or tampered
# binary is worse than a failed install, and it is one extra request.
if curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt" 2>/dev/null; then
	expected=$(grep " $asset\$" "$tmp/checksums.txt" | awk '{print $1}')
	if [ -n "$expected" ]; then
		if command -v sha256sum >/dev/null 2>&1; then
			actual=$(sha256sum "$tmp/$asset" | awk '{print $1}')
		elif command -v shasum >/dev/null 2>&1; then
			actual=$(shasum -a 256 "$tmp/$asset" | awk '{print $1}')
		else
			actual=""
		fi
		if [ -n "$actual" ] && [ "$actual" != "$expected" ]; then
			die "checksum mismatch for $asset — refusing to install"
		fi
		[ -n "$actual" ] && say "checksum ok"
	fi
else
	say "warning: no checksums.txt in this release, skipping verification"
fi

tar -xzf "$tmp/$asset" -C "$tmp" || die "could not unpack $asset"
[ -f "$tmp/$BIN" ] || die "$asset did not contain $BIN"

mkdir -p "$INSTALL_DIR" || die "could not create $INSTALL_DIR"
# Written to a neighbouring name and moved, so an interrupted install cannot
# leave a half-written binary where a working one used to be.
mv "$tmp/$BIN" "$INSTALL_DIR/$BIN.new" || die "could not write to $INSTALL_DIR"
chmod +x "$INSTALL_DIR/$BIN.new"
mv "$INSTALL_DIR/$BIN.new" "$INSTALL_DIR/$BIN"

say "installed $("$INSTALL_DIR/$BIN" -version) to $INSTALL_DIR/$BIN"

case ":$PATH:" in
	*":$INSTALL_DIR:"*) say "run: $BIN" ;;
	*)
		say ""
		say "$INSTALL_DIR is not on your PATH. Add it:"
		say "  export PATH=\"\$PATH:$INSTALL_DIR\""
		;;
esac
