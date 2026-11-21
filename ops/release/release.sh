#!/usr/bin/env bash
# Releases gs off GitHub (design/21_self_hosting.md, "Releases").
#
#   ops/release/release.sh run [<tag>]   build and publish now: every unpublished
#                                        v* tag, or just <tag> (rebuilt even if
#                                        published)
#   ops/release/release.sh schedule      create or update the daily Cloud
#                                        Scheduler run that builds missed tags
#
# A release starts with a native tag: gs tag create v0.5.0 --slice gitslice/gitslice
# (the release automation in .gitslice/automations.yaml builds it).
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
project="${GCP_PROJECT:-unified-surfer-486904-i6}"
region="${GCP_REGION:-us-west1}"
job="gs-release"

service_account() {
  local number
  number="$(gcloud projects describe "$project" --format='value(projectNumber)')"
  echo "${number}-compute@developer.gserviceaccount.com"
}

case "${1:-}" in
  run)
    subs=()
    if [ -n "${2:-}" ]; then
      if [[ ! "$2" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
        echo "invalid release tag: $2" >&2
        exit 1
      fi
      subs=("--substitutions=_TAG=$2,_FORCE=1")
    fi
    gcloud builds submit --project "$project" --no-source --config "$here/cloudbuild.yaml" "${subs[@]+"${subs[@]}"}"
    ;;
  schedule)
    account="$(service_account)"
    body="$(mktemp)"
    # The Cloud Build API takes the same build as JSON.
    python3 - "$here/cloudbuild.yaml" > "$body" <<'PY'
import json, sys
try:
    import yaml
except ImportError:
    sys.exit("python3 yaml module is required (pip install pyyaml)")
print(json.dumps(yaml.safe_load(open(sys.argv[1]))))
PY
    uri="https://cloudbuild.googleapis.com/v1/projects/${project}/builds"
    # create takes --headers; update takes --update-headers.
    if gcloud scheduler jobs describe "$job" --project "$project" --location "$region" >/dev/null 2>&1; then
      verb=update headers=--update-headers
    else
      verb=create headers=--headers
    fi
    # The release automation builds each new tag; this only catches a missed one.
    gcloud scheduler jobs "$verb" http "$job" --project "$project" --location "$region" \
      --schedule "${RELEASE_SCHEDULE:-30 6 * * *}" --time-zone UTC --uri "$uri" --http-method POST \
      "$headers" "Content-Type=application/json" --message-body-from-file "$body" \
      --oauth-service-account-email "$account" \
      --description "Build gs releases for tags the release automation missed (ops/release)"
    rm -f "$body"
    ;;
  *)
    sed -n '2,15p' "$0"
    exit 2
    ;;
esac
