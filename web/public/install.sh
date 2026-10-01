#!/bin/sh
# Install the Gitslice CLI (gs) from the latest release.
#
#   curl -fsSL https://gitslice.io/install.sh | sh
#
# Environment:
#   GS_INSTALL_DIR  where to put gs (default: $HOME/.local/bin)
#   GS_VERSION      a release tag such as v0.1.0 (default: latest)
#   GS_DOWNLOAD_BASE  alternative download base URL (mirrors, testing)
set -eu

releases="https://gitslice.io/releases"
install_dir="${GS_INSTALL_DIR:-$HOME/.local/bin}"
version="${GS_VERSION:-latest}"

fail() {
  echo "gs install: $*" >&2
  exit 1
}

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) fail "unsupported OS $(uname -s); download a build from $releases" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) fail "unsupported CPU $(uname -m); download a build from $releases" ;;
esac

if [ -n "${GS_DOWNLOAD_BASE:-}" ]; then
  base="$GS_DOWNLOAD_BASE"
elif [ "$version" = latest ]; then
  base="$releases/latest/download"
else
  base="$releases/download/$version"
fi
asset="gs_${os}_${arch}.tar.gz"

if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -q "$1" -O "$2"; }
else
  fail "curl or wget is required"
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

echo "Downloading $asset ($version)..."
fetch "$base/$asset" "$tmp/$asset" || fail "download failed: $base/$asset"
fetch "$base/checksums.txt" "$tmp/checksums.txt" || fail "download failed: $base/checksums.txt"

expected="$(grep " $asset\$" "$tmp/checksums.txt" | cut -d' ' -f1)"
[ -n "$expected" ] || fail "no checksum for $asset"
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$tmp/$asset" | cut -d' ' -f1)"
else
  actual="$(shasum -a 256 "$tmp/$asset" | cut -d' ' -f1)"
fi
[ "$expected" = "$actual" ] || fail "checksum mismatch for $asset"

tar -C "$tmp" -xzf "$tmp/$asset"
mkdir -p "$install_dir"
install -m 0755 "$tmp/gs" "$install_dir/gs" 2>/dev/null || { cp "$tmp/gs" "$install_dir/gs" && chmod 0755 "$install_dir/gs"; }

echo "Installed $("$install_dir/gs" version | head -n 1) to $install_dir/gs"
case ":$PATH:" in
  *":$install_dir:"*) ;;
  *) echo "Add it to your PATH: export PATH=\"$install_dir:\$PATH\"" ;;
esac
echo "Next: gs auth register-agent --username <name> --email <owner-email>  (agents)"
echo "  or: gs auth login  (humans)"
