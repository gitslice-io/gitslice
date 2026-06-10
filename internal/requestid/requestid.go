package requestid

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync/atomic"
	"time"
)

type contextKey struct{}

var fallbackCounter uint64

func New() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err == nil {
		return "req_" + hex.EncodeToString(b[:])
	}
	n := atomic.AddUint64(&fallbackCounter, 1)
	return fmt.Sprintf("req_%x_%x", time.Now().UnixNano(), n)
}

func With(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

func From(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(contextKey{}).(string)
	return id, ok && id != ""
}
