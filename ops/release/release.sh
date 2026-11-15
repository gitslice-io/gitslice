#!/usr/bin/env bash
# Releases gs off GitHub (design/21_self_hosting.md, "Releases").
#
#   ops/release/release.sh run [<tag>]   build and publish now: every unpublished
#                                        v* tag, or just <tag> (rebuilt even if
#                                        published)
#   ops/release/release.sh webhook       create or update the Cloud Build webhook
#                                        trigger that the slice's tag.created
#                                        webhook calls, and write its URL to
#                                        $RELEASE_WEBHOOK_URL_FILE
#   ops/release/release.sh schedule      create or update the daily Cloud
#                                        Scheduler run that builds missed tags
#
# A release starts with a native tag: gs tag create v0.5.0 --slice gitslice/gitslice
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
project="${GCP_PROJECT:-unified-surfer-486904-i6}"
region="${GCP_REGION:-us-west1}"
job="gs-release"
trigger="gs-release-webhook"
secret_name="gs-release-webhook-secret"
key_name="gs-release-webhook"
url_file="${RELEASE_WEBHOOK_URL_FILE:-$HOME/.config/gitslice-release/webhook-url}"

service_account() {
  local number
  number="$(gcloud projects describe "$project" --format='value(projectNumber)')"
  echo "${number}-compute@developer.gserviceaccount.com"
}

case "${1:-}" in
  run)
    subs=""
    if [ -n "${2:-}" ]; then subs="--substitutions=_TAG=$2,_FORCE=1"; fi
    gcloud builds submit --project "$project" --no-source --config "$here/cloudbuild.yaml" $subs
    ;;
  webhook)
    account="$(service_account)"
    # The trigger's shared secret, which the caller passes as ?secret=.
    if ! gcloud secrets describe "$secret_name" --project "$project" >/dev/null 2>&1; then
      openssl rand -hex 32 | tr -d '\n' | gcloud secrets create "$secret_name" --project "$project" \
        --replication-policy automatic --data-file=- >/dev/null
    fi
    gcloud secrets add-iam-policy-binding "$secret_name" --project "$project" \
      --member "serviceAccount:service-$(gcloud projects describe "$project" --format='value(projectNumber)')@gcp-sa-cloudbuild.iam.gserviceaccount.com" \
      --role roles/secretmanager.secretAccessor >/dev/null
    version="$(gcloud secrets versions list "$secret_name" --project "$project" --filter state=ENABLED --sort-by ~createTime --limit 1 --format 'value(name)')"
    # An API key that can only call Cloud Build, which the caller passes as ?key=.
    gcloud services enable apikeys.googleapis.com --project "$project"
    key="$(gcloud services api-keys list --project "$project" --filter "displayName=$key_name" --format 'value(name)' | head -n 1)"
    if [ -z "$key" ]; then
      gcloud services api-keys create --project "$project" --display-name "$key_name" \
        --api-target service=cloudbuild.googleapis.com >/dev/null 2>&1
      key="$(gcloud services api-keys list --project "$project" --filter "displayName=$key_name" --format 'value(name)' | head -n 1)"
    fi
    # The trigger runs cloudbuild.yaml with _TAG from the event; anything but
    # tag.created (a ping, say) starts nothing.
    if gcloud builds triggers describe "$trigger" --project "$project" >/dev/null 2>&1; then
      gcloud builds triggers delete "$trigger" --project "$project" --quiet
    fi
    gcloud builds triggers create webhook --project "$project" --name "$trigger" \
      --description "Build a gs release when gitslice/gitslice gets a tag (ops/release)" \
      --secret "projects/$project/secrets/$secret_name/versions/${version##*/}" \
      --service-account "projects/$project/serviceAccounts/$account" \
      --inline-config "$here/cloudbuild.yaml" \
      --substitutions '_TAG=$(body.tag.name),_EVENT=$(body.event)' \
      --subscription-filter '_EVENT == "tag.created"' >/dev/null
    mkdir -p "$(dirname "$url_file")"
    (
      umask 077
      printf 'https://cloudbuild.googleapis.com/v1/projects/%s/triggers/%s:webhook?key=%s&secret=%s\n' \
        "$project" "$trigger" \
        "$(gcloud services api-keys get-key-string "$key" --project "$project" --format 'value(keyString)')" \
        "$(gcloud secrets versions access latest --secret "$secret_name" --project "$project")" > "$url_file"
    )
    echo "trigger $trigger is ready; its URL (with the key and secret) is in $url_file"
    echo "subscribe the slice: gs webhook create --slice gitslice/gitslice --event tag.created --url \"\$(cat $url_file)\" --quiet"
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
    # Tags arrive by webhook; this only catches a missed one.
    gcloud scheduler jobs "$verb" http "$job" --project "$project" --location "$region" \
      --schedule "${RELEASE_SCHEDULE:-30 6 * * *}" --time-zone UTC --uri "$uri" --http-method POST \
      "$headers" "Content-Type=application/json" --message-body-from-file "$body" \
      --oauth-service-account-email "$account" \
      --description "Build gs releases for tags the webhook missed (ops/release)"
    rm -f "$body"
    ;;
  *)
    sed -n '2,15p' "$0"
    exit 2
    ;;
esac
