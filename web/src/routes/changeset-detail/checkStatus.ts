// A check run's status, apart from the panel that shows it: the server render
// needs to know whether a run is finished (see the changeset route's loader)
// without bringing the panel into every page's script.

const terminalCheckStatuses = new Set(["passed", "failed", "errored", "skipped", "canceled"]);

export function normalizeCheckStatus(status?: string) {
  return (status || "queued").toLowerCase();
}

export function isTerminalCheckStatus(status?: string) {
  return terminalCheckStatuses.has(normalizeCheckStatus(status));
}
