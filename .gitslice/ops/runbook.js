// Worker script of the deployment runbooks in ../automations.yaml.
//
// onEvent hands the run to the Cloud Build webhook trigger gs-ops-webhook
// (ops/automation) along with the run's callback URL, and keeps the run open.
// The build POSTs JSON to that URL as it goes:
//   {"lines": ["..."]}                      progress, shown in the run's log
//   {"outcome": "succeeded" | "failed", "lines": [...]}   the result
// so `gs auto run <id> --follow` shows the operation and fails when it fails.
// A build that never calls back ends the run at the automation's timeout.

export default {
  async onEvent(event, gs) {
    const res = await fetch(gs.secret("OPS_WEBHOOK_URL"), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        event: "manual",
        automation: { id: event.automation?.id },
        sender: { username: event.sender?.username },
        inputs: event.inputs ?? {},
        callback: gs.callbackUrl(),
      }),
    });
    if (!res.ok) {
      throw new Error(`Cloud Build webhook answered ${res.status}: ${(await res.text()).slice(0, 300)}`);
    }
    console.log("handed to Cloud Build (trigger gs-ops-webhook); waiting for the build to report");
    gs.waitForCallback();
  },

  async onCallback(request, gs) {
    let body;
    try {
      body = await request.json();
    } catch {
      return new Response("expected JSON\n", { status: 400 });
    }
    for (const line of Array.isArray(body.lines) ? body.lines : []) {
      console.log(String(line));
    }
    if (body.outcome === "succeeded") {
      gs.finish();
    } else if (body.outcome) {
      throw new Error(`the operation ${body.outcome}; see the log above`);
    }
    return new Response("ok\n");
  },
};
