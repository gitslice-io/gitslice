import type { AgentDaemon } from "../api/types";

// The name a daemon is shown by: what it was renamed to, else the name it
// registered with (gs agent start --name), else its id.
export function daemonName(daemon: AgentDaemon | undefined, fallback = ""): string {
  return daemon?.displayName || daemon?.name || daemon?.id || fallback;
}

// Whether the viewer can start conversations on the daemon. The server lists
// the daemons of agents the viewer owns too, to name them and make them a
// slice's CI daemon, but only the identity that registered a daemon can host
// conversations on it.
export function canHostConversations(daemon: AgentDaemon): boolean {
  return daemon.mine !== false && daemon.runtime?.toLowerCase() !== "ci";
}
