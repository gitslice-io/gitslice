package storage

import "time"

// DaemonStaleAfter is how long a daemon may go unheard of and still count as
// online. A connected daemon sends a heartbeat every 15 seconds and a
// detached one polls every 30, so a daemon silent for this long is gone, even
// if the server that saw it leave never got to say so (an instance stopped or
// replaced without closing its streams used to leave its daemons "online" for
// ever).
const DaemonStaleAfter = 90 * time.Second

// EffectiveDaemonStatus is the status a daemon is shown with: the stored
// status, except that "online" needs a recent sign of life.
func EffectiveDaemonStatus(status string, lastSeen, now time.Time) string {
	if status == "online" && now.Sub(lastSeen) > DaemonStaleAfter {
		return "offline"
	}
	return status
}
