#!/usr/bin/env bash
# Builds gs release archives for the tags that need it. Run by
# ops/release/cloudbuild.yaml in a golang image, from /workspace.
#
# In:  /workspace/published.txt (tags already in R2), TAG (the tag a webhook
#      named, or empty for all), FORCE (1: rebuild TAG even if published),
#      SINCE (only build tags newer than this), GIT_URL, MODULE_DIR.
# Out: /workspace/dist/<tag>/{gs_<os>_<arch>.tar.gz|.zip, checksums.txt},
#      /workspace/built.txt (tags built), /workspace/latest.json.
# The archive layout matches .github/workflows/release.yml, so install.sh and
# gs upgrade work with either.
set -euo pipefail

cd /workspace
touch published.txt
: > built.txt

tags_file="$(mktemp)"
list_tags() {
  git ls-remote --tags "$GIT_URL" 'refs/tags/v*' | awk '{ sub("refs/tags/", "", $2); print $2 }' | grep -v '\^{}$' | sort -u > "$tags_file"
}
list_tags
# A webhook fires as the tag is created; give the Git endpoint a moment to
# list it rather than miss the release.
if [ -n "${TAG:-}" ]; then
  for _ in 1 2 3 4 5 6 7 8 9 10 11 12; do
    grep -qx "$TAG" "$tags_file" && break
    echo "waiting for $TAG at $GIT_URL"
    sleep 10
    list_tags
  done
  grep -qx "$TAG" "$tags_file" || { echo "no tag $TAG at $GIT_URL" >&2; exit 1; }
fi

# semver_gt A B: true when release tag A is newer than B (vMAJOR.MINOR.PATCH).
semver_gt() {
  [ "$1" = "$2" ] && return 1
  [ "$(printf '%s\n%s\n' "${1#v}" "${2#v}" | sort -V | tail -n 1)" = "${1#v}" ]
}

pending=()
if [ -n "${TAG:-}" ] && [ "${FORCE:-}" = 1 ]; then
  # release.sh run <tag>: rebuild it even if it is published.
  pending=("$TAG")
else
  # A webhook names one tag; the daily reconcile names none. Either way only
  # release tags (vX.Y.Z) newer than SINCE that are not published are built,
  # so a repeated or stray event builds nothing.
  while read -r tag; do
    [ -n "${TAG:-}" ] && [ "$tag" != "$TAG" ] && continue
    [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || continue
    semver_gt "$tag" "$SINCE" || continue
    grep -qx "$tag" published.txt && continue
    pending+=("$tag")
  done < "$tags_file"
  if [ -n "${TAG:-}" ] && [ "${#pending[@]}" -eq 0 ]; then
    echo "$TAG is not an unpublished release tag newer than $SINCE"
  fi
fi

# latest.json names the newest tag that is (or will be) published.
latest=""
for tag in $(cat published.txt) "${pending[@]}"; do
  [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || continue
  if [ -z "$latest" ] || semver_gt "$tag" "$latest"; then latest="$tag"; fi
done
printf '{"tag":"%s"}\n' "$latest" > latest.json

if [ "${#pending[@]}" -eq 0 ]; then
  echo "no tags to build (latest $latest)"
  exit 0
fi

apt-get update -qq && apt-get install -y -qq zip >/dev/null

for tag in "${pending[@]}"; do
  echo "== building $tag"
  src="$(mktemp -d)"
  git clone -q --depth 1 --branch "$tag" "$GIT_URL" "$src"
  commit="$(git -C "$src" rev-parse HEAD)"
  date="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  pkg="gitslice.io/gitslice/internal/cli"
  ldflags="-s -w -X ${pkg}.Version=${tag} -X ${pkg}.BuildCommit=${commit} -X ${pkg}.BuildDate=${date}"
  out="/workspace/dist/$tag"
  mkdir -p "$out"
  (
    cd "$src/$MODULE_DIR"
    for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
      os="${target%/*}"; arch="${target#*/}"
      name="gs_${os}_${arch}"
      bin="gs"; [ "$os" = windows ] && bin="gs.exe"
      work="$(mktemp -d)"
      CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" GOFLAGS=-mod=readonly go build -trimpath -ldflags "$ldflags" -o "$work/$bin" ./cmd/gs
      cp LICENSE* "$work/" 2>/dev/null || true
      if [ "$os" = windows ]; then
        (cd "$work" && zip -q "$out/${name}.zip" ./*)
      else
        tar -C "$work" -czf "$out/${name}.tar.gz" .
      fi
    done
  )
  (cd "$out" && sha256sum gs_* > checksums.txt)
  # Smoke test before anything is published.
  check="$(mktemp -d)"
  tar -C "$check" -xzf "$out/gs_linux_amd64.tar.gz"
  "$check/gs" version | grep -q "gs version ${tag}" || { echo "built gs does not report $tag" >&2; exit 1; }
  ls -l "$out"
  echo "$tag" >> built.txt
done
