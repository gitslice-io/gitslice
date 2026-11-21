#!/usr/bin/env bash
# Cloudflare runbooks of ../automations.yaml (deploy-dispatcher,
# deploy-doorbell, deploy-web, rollback-web,
# cloudflare-selftest). They run as
# bash automations on Gitslice's own container runtime (runtime node, egress
# http-gate), from a checkout of /gitslice/gitslice at main.
#
# CLOUDFLARE_API_TOKEN is a Gitslice secret. Here it is only a sealed
# placeholder: the gate swaps in the real token on requests to
# api.cloudflare.com and nowhere else, so neither this script nor wrangler
# ever holds it.
#
# Containers have no Docker daemon, so the dispatcher deploys with its
# container images pinned by digest (trigger-dispatch/containers/
# images.lock.json), already in Cloudflare's registry. Rebuilding them (a
# change under trigger-dispatch/containers/ or cmd/gs-exec) is a local
# `npm run deploy` with Docker, then an update of the lock file.
set -euo pipefail

op="${OP:?OP names the operation}"
account="02fa014ee5c259f9cb1e01cf224ae197"
export CLOUDFLARE_ACCOUNT_ID="$account" WRANGLER_SEND_METRICS=false CI=1

api() {
  local path="$1"
  shift
  curl -fsS --retry 3 --retry-all-errors -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN" \
    -H 'Content-Type: application/json' "https://api.cloudflare.com/client/v4/accounts/$account/$path" "$@"
}
json() { node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const v=JSON.parse(s);console.log(eval(process.argv[1])??"")})' "$1"; }

# deploy_config <dir> <node expression editing cfg> writes the Worker's
# wrangler.jsonc, edited, to <dir>/wrangler.automation.json.
deploy_config() {
  node -e '
    const fs = require("fs");
    const [dir, edit] = process.argv.slice(1);
    const src = fs.readFileSync(dir + "/wrangler.jsonc", "utf8").split("\n").filter((l) => !l.trim().startsWith("//")).join("\n");
    const cfg = JSON.parse(src);
    delete cfg["$schema"];
    eval(edit);
    fs.writeFileSync(dir + "/wrangler.automation.json", JSON.stringify(cfg, null, 2));
  ' "$1" "$2"
}

# upload_and_deploy <dir>: a new version of the Worker at 100%.
upload_and_deploy() {
  local dir="$1" out version
  out="$(cd "$dir" && npx wrangler versions upload -c wrangler.automation.json --message "gitslice automation $op" 2>&1)" || { echo "$out"; return 1; }
  echo "$out" | grep -v -i token | tail -4
  version="$(echo "$out" | sed -n 's/.*Worker Version ID: *\([0-9a-f-]\{36\}\).*/\1/p' | tail -1)"
  [ -n "$version" ] || { echo "no version id in wrangler's output" >&2; return 1; }
  echo "deploying version $version"
  (cd "$dir" && npx wrangler versions deploy "$version@100%" -c wrangler.automation.json -y 2>&1 | grep -v -i token | tail -3)
}

# deploy_web <env> <site> builds the web app from this checkout and deploys
# it with wrangler to <env> of web/wrangler.jsonc. Its build variables are
# public and live in web/build.<env>.vars; its secrets (CLERK_SECRET_KEY) are
# Worker secrets, which a deploy keeps.
deploy_web() {
  local env="$1" site="$2"
  (cd web && npm ci --no-audit --no-fund --loglevel=error)
  set -a
  . "web/build.$env.vars"
  set +a
  (cd web && npm run build 2>&1 | tail -4)
  (cd web && npx wrangler deploy --env "$env" --message "gitslice automation $op" 2>&1 | grep -v -i token | tail -6)
  curl -fsS -o /dev/null --retry 3 --retry-all-errors "$site/" && echo "$site/ answers"
}

case "$op" in
  cloudflare-selftest)
    echo "node $(node -v)"
    n="$(api workers/scripts | json 'v.result.length')"
    echo "the token reaches Cloudflare through the gate: $n Workers in the account"
    ;;

  deploy-dispatcher)
    (cd trigger-dispatch && npm ci --no-audit --no-fund --loglevel=error)
    # The images as built and pushed before, pinned by digest: no build.
    deploy_config trigger-dispatch '
      const lock = JSON.parse(fs.readFileSync(dir + "/containers/images.lock.json", "utf8"));
      for (const c of cfg.containers ?? []) {
        c.images = Object.fromEntries(Object.keys(c.images ?? {}).map((name) => {
          if (!lock[name]) throw new Error("containers/images.lock.json has no image " + name);
          return [name, { image: lock[name] }];
        }));
      }'
    echo "deploying the dispatcher; this run is served by it, so its end may be reported by the new version"
    upload_and_deploy trigger-dispatch
    ;;

  deploy-doorbell)
    (cd doorbell && npm ci --no-audit --no-fund --loglevel=error)
    # The Cloudflare CI runners (design/32) stay off: no runner container,
    # no runner daemon, as deployed today.
    deploy_config doorbell 'delete cfg.containers; cfg.vars.RUNNER_DAEMON_ID = "";'
    (cd doorbell && npx wrangler deploy -c wrangler.automation.json 2>&1 | grep -v -i token | tail -6)
    ;;

  deploy-web)
    deploy_web production https://gitslice.io
    ;;

  rollback-web)
    version="${INPUT_VERSION:?input version is required}"
    [[ "$version" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]] || { echo "version must be a Worker version id" >&2; exit 1; }
    echo "recent deployments of gitslice-web-production:"
    api workers/scripts/gitslice-web-production/deployments | json 'v.result.deployments.slice(0, 5).map(d => d.created_on + "  " + d.versions.map(x => x.version_id + "@" + x.percentage + "%").join(" ")).join("\n")'
    api workers/scripts/gitslice-web-production/deployments -X POST \
      -d "{\"strategy\":\"percentage\",\"versions\":[{\"version_id\":\"$version\",\"percentage\":100}],\"annotations\":{\"workers/message\":\"gitslice automation rollback-web\"}}" >/dev/null
    echo "gitslice-web-production serves version $version; the next deploy-web or push to main moves it back"
    curl -fsS -o /dev/null https://gitslice.io/ && echo "https://gitslice.io/ answers"
    ;;

  *)
    echo "not an operation: $op" >&2
    exit 1
    ;;
esac
