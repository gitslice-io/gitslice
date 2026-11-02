package checkexec

import (
	"os"
	"strings"
)

// HostNetwork reports whether checks run on the host's network
// (GITSLICE_CHECK_HOST_NETWORK=1). A runner whose container engine cannot
// set up NAT for bridge networks, such as Docker inside a Cloudflare
// container (no iptables, design/32_ci_dispatch_on_cloudflare.md layer 2),
// sets it: image builds, checks that may use the network and services then
// use the host's network, and services are reached by name at 127.0.0.1.
// Checks without network access stay on --network=none.
func HostNetwork() bool {
	return strings.TrimSpace(os.Getenv("GITSLICE_CHECK_HOST_NETWORK")) == "1"
}

const hostNetwork = "host"
