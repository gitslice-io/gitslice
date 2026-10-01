#!/usr/bin/env bash
# Phase 1 of design/21_self_hosting.md: create the `gitslice` organization and
# the public slice gitslice/gitslice, then import the GitHub history into
# /gitslice/gitslice. Safe to re-run: every step checks before it acts, and
# the import only replays commits it has not imported yet.
#
#   OPERATOR_HOME=~/.config/gitslice-operator MIRROR_HOME=~/.config/gitslice-mirror \
#     ops/selfhost/phase1.sh
#
# OPERATOR_HOME and MIRROR_HOME are HOME directories whose .gitslice/config.json
# hold the operator's and the mirror bot's credentials. The operator must be
# listed in the server's GITSLICE_OPERATOR_SUBJECTS.
#
# Optional: GS (gs binary), MIRROR_USER (default gitslice-mirror), OWNERS
# (extra owner usernames, space separated), SOURCE (default
# gitslice-io/gitslice), GIT_BASE (default https://gitslice.io/git), ORG and
# SLICE (default gitslice, gitslice; use others to rehearse on staging).
set -euo pipefail

GS=${GS:-gs}
MIRROR_USER=${MIRROR_USER:-gitslice-mirror}
SOURCE=${SOURCE:-gitslice-io/gitslice}
GIT_BASE=${GIT_BASE:-https://gitslice.io/git}
ORG=${ORG:-gitslice}
SLICE=${SLICE:-gitslice}
: "${OPERATOR_HOME:?set OPERATOR_HOME}" "${MIRROR_HOME:?set MIRROR_HOME}"

op() { HOME="$OPERATOR_HOME" "$GS" "$@"; }
mirror() { HOME="$MIRROR_HOME" "$GS" "$@"; }
step() { printf '\n== %s\n' "$*"; }

step "organization $ORG"
if op account members "$ORG" >/dev/null 2>&1; then
  echo "exists"
else
  op account create-org "$ORG"
fi
for owner in ${OWNERS:-}; do
  op account set-member "$ORG" "$owner" --role owner
done
op account set-member "$ORG" "$MIRROR_USER" --role writer

step "folder /$ORG/$SLICE and slice $ORG/$SLICE"
if op slice info "$ORG/$SLICE" >/dev/null 2>&1; then
  echo "slice exists"
else
  # A slice's included path must exist first; gs shell creates it with a
  # changeset in the organization's home slice.
  printf 'mkdir /%s/%s\nquit\n' "$ORG" "$SLICE" | op shell --slice "$ORG/home" --no-color >/dev/null
  op slice create "$ORG/$SLICE" --include "/$ORG/$SLICE" --visibility public
fi

step "import $SOURCE (new commits only)"
mirror import "$SOURCE" --deep --mount "/$ORG/$SLICE" --slice "$ORG/$SLICE"

step "verify: anonymous clone matches GitHub main"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
git clone -q "$GIT_BASE/$ORG/$SLICE.git" "$work/projected"
git clone -q --bare "https://github.com/$SOURCE.git" "$work/github"
projected=$(git -C "$work/projected" rev-parse "HEAD:$ORG/$SLICE")
github=$(git -C "$work/github" rev-parse "main^{tree}")
if [ "$projected" = "$github" ]; then
  echo "trees match ($github)"
else
  echo "DRIFT: Gitslice tree $projected, GitHub tree $github" >&2
  exit 1
fi

cat <<'NEXT'

Next (design/21_self_hosting.md, Phase 1):
  gh secret set GITSLICE_MIRROR_TOKEN        # the mirror bot's API key
  gh variable set GITSLICE_IMPORT_ENABLED --body true
  gs tag create v0.2.0 --slice gitslice/gitslice --commit <native id of b00271e>
NEXT
