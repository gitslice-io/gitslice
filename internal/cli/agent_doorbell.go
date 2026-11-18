package cli

import (
	"context"
	"time"

	"gitslice.io/gitslice/internal/agentd"
)

const doorbellInitialReadyWait = agentd.DoorbellInitialReadyWait

type doorbellListener = agentd.DoorbellListener

func listenDoorbell(ctx context.Context, waitURL, token string, say func(string)) *doorbellListener {
	return agentd.ListenDoorbell(ctx, waitURL, token, say)
}

func listenDoorbellWithTLS(ctx context.Context, waitURL, token string, requireTLS bool, say func(string)) *doorbellListener {
	return agentd.ListenDoorbellWithTLS(ctx, waitURL, token, requireTLS, insecureFromContext, say)
}

func normalizeDoorbellURL(waitURL string, requireTLS, insecureRequested bool) (string, error) {
	return agentd.NormalizeDoorbellURL(waitURL, requireTLS, insecureRequested)
}

func originFor(waitURL string) string {
	return agentd.OriginForDoorbell(waitURL)
}

func doorbellDisabled() bool {
	return agentd.DoorbellDisabled()
}

var _ = time.Second
