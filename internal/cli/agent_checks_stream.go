package cli

import (
	"gitslice.io/gitslice/internal/agentd"
)

type checkoutStreamer = agentd.CheckoutStreamer

var errNoTreeCheckout = agentd.ErrNoTreeCheckout
