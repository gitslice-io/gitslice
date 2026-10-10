#!/bin/sh
# Install the Gitslice CLI (gs) from the latest release.
#
#   curl -fsSL https://gitslice.io/install.sh | sh
#
# It downloads the prebuilt gs for this OS and CPU from gitslice.io, checks it
# against the release's checksums, and installs it. Where there is no prebuilt
# gs (or the download fails) and Go is installed, it builds gs from source
# instead: go install gitslice.io/gitslice/cmd/gs@<version>.
#
# Environment:
#   GS_INSTALL_DIR    where to put gs (default: $HOME/.local/bin)
#   GS_VERSION        a release tag such as v0.4.3 (default: latest)
#   GS_FROM_SOURCE    1 to build from source with go install instead
#   GS_DOWNLOAD_BASE  alternative download base URL (mirrors, testing)
set -eu

releases="https://gitslice.io/releases"
module="gitslice.io/gitslice/cmd/gs"
install_dir="${GS_INSTALL_DIR:-$HOME/.local/bin}"
version="${GS_VERSION:-latest}"

fail() {
  echo "gs install: $*" >&2
  exit 1
}

finish() {
  bin="$1"
  echo "Installed $("$bin" version | head -n 1) to $bin"
  case ":$PATH:" in
    *":$install_dir:"*) ;;
    *) echo "Add it to your PATH: export PATH=\"$install_dir:\$PATH\"" ;;
  esac
  echo "Next: gs auth register-agent --username <name> --email <owner-email>  (agents)"
  echo "  or: gs auth login  (humans)"
  echo "Later, gs upgrade installs newer releases."
}

# Build and install gs with the Go toolchain, from the gitslice.io module.
from_source() {
  command -v go >/dev/null 2>&1 || fail "$1; install Go (https://go.dev/dl) to build gs from source, or download a build from $releases"
  echo "$1; building gs from source: go install $module@$version"
  mkdir -p "$install_dir"
  GOBIN="$install_dir" go install "$module@$version" || fail "go install $module@$version failed"
  bin="$install_dir/gs"
  [ -x "$bin" ] || bin="$install_dir/gs.exe"
  finish "$bin"
  exit 0
}

if [ "${GS_FROM_SOURCE:-}" = 1 ]; then
  from_source "GS_FROM_SOURCE=1"
fi

ext=tar.gz
exe=gs
case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  MINGW* | MSYS* | CYGWIN*) os=windows ext=zip exe=gs.exe ;;
  *) from_source "no prebuilt gs for $(uname -s)" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) from_source "no prebuilt gs for $(uname -m) CPUs" ;;
esac

if [ -n "${GS_DOWNLOAD_BASE:-}" ]; then
  base="$GS_DOWNLOAD_BASE"
elif [ "$version" = latest ]; then
  base="$releases/latest/download"
else
  base="$releases/download/$version"
fi
asset="gs_${os}_${arch}.${ext}"

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
fetch "$base/$asset" "$tmp/$asset" || from_source "download failed: $base/$asset"
fetch "$base/checksums.txt" "$tmp/checksums.txt" || fail "download failed: $base/checksums.txt"

expected="$(grep " $asset\$" "$tmp/checksums.txt" | cut -d' ' -f1)"
[ -n "$expected" ] || fail "no checksum for $asset"
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$tmp/$asset" | cut -d' ' -f1)"
else
  actual="$(shasum -a 256 "$tmp/$asset" | cut -d' ' -f1)"
fi
[ "$expected" = "$actual" ] || fail "checksum mismatch for $asset"

if [ "$ext" = zip ]; then
  command -v unzip >/dev/null 2>&1 || fail "unzip is required to install the Windows build"
  unzip -q -o "$tmp/$asset" -d "$tmp"
else
  tar -C "$tmp" -xzf "$tmp/$asset"
fi
mkdir -p "$install_dir"
install -m 0755 "$tmp/$exe" "$install_dir/$exe" 2>/dev/null || { cp "$tmp/$exe" "$install_dir/$exe" && chmod 0755 "$install_dir/$exe"; }

finish "$install_dir/$exe"
