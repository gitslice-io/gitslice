import sys, yaml
root = sys.argv[1]
build = yaml.safe_load(open(root + "/ops/automation/cloudbuild.yaml"))
trigger = {
    "name": "gs-ops-webhook",
    "description": "Deployment operations started by the manual automations in .gitslice/automations.yaml (ops/automation)",
    "serviceAccount": "projects/unified-surfer-486904-i6/serviceAccounts/147550290058-compute@developer.gserviceaccount.com",
    "webhookConfig": {"secret": "projects/unified-surfer-486904-i6/secrets/gs-ops-webhook-secret/versions/1"},
    "substitutions": {
        "_EVENT": "$(body.event)",
        "_PAYLOAD": "$(body)",
    },
    "filter": '_EVENT == "manual"',
    "build": build,
}
header = (
    "# Cloud Build webhook trigger gs-ops-webhook, generated from cloudbuild.yaml\n"
    "# (python3 ops/automation/gentrigger.py . after changing it). Import with:\n"
    "#   gcloud builds triggers import --source=ops/automation/trigger.yaml \\\n"
    "#     --project unified-surfer-486904-i6 --region=global\n"
)
open(root + "/ops/automation/trigger.yaml", "w").write(header + yaml.safe_dump(trigger, sort_keys=False, width=200))
