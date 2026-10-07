#!/usr/bin/env bash
# Releases gs off GitHub (design/21_self_hosting.md, "Releases").
#
#   ops/release/release.sh run [<tag>]   build and publish now: every unpublished
#                                        v* tag, or just <tag> (rebuilt even if
#                                        published)
#   ops/release/release.sh schedule      create or update the Cloud Scheduler job
#                                        that runs the build every 15 minutes
#
# A release starts with a native tag: gs tag create v0.5.0 --slice gitslice/gitslice
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
project="${GCP_PROJECT:-unified-surfer-486904-i6}"
region="${GCP_REGION:-us-west1}"
job="gs-release"

case "${1:-}" in
  run)
    subs=""
    if [ -n "${2:-}" ]; then subs="--substitutions=_TAG=$2"; fi
    gcloud builds submit --project "$project" --no-source --config "$here/cloudbuild.yaml" $subs
    ;;
  schedule)
    number="$(gcloud projects describe "$project" --format='value(projectNumber)')"
    account="${number}-compute@developer.gserviceaccount.com"
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
    if gcloud scheduler jobs describe "$job" --project "$project" --location "$region" >/dev/null 2>&1; then
      verb=update
    else
      verb=create
    fi
    gcloud scheduler jobs "$verb" http "$job" --project "$project" --location "$region" \
      --schedule "*/15 * * * *" --uri "$uri" --http-method POST \
      --headers "Content-Type=application/json" --message-body-from-file "$body" \
      --oauth-service-account-email "$account" \
      --description "Build gs releases from Gitslice tags (ops/release)"
    rm -f "$body"
    ;;
  *)
    sed -n '2,12p' "$0"
    exit 2
    ;;
esac
