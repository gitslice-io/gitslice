package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
)

const diffCacheVersion = 1

func diffCacheKey(changesetID, fromPatchsetID, toPatchsetID string) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s", changesetID, fromPatchsetID, toPatchsetID)))
	return fmt.Sprintf("cache/diff/v%d/%s", diffCacheVersion, hex.EncodeToString(digest[:]))
}

func (s *ChangesetService) readDiffCache(ctx context.Context, key string) (string, bool) {
	r, err := s.ObjectStore.Get(ctx, key, 0, 0)
	if err != nil {
		return "", false
	}
	data, readErr := io.ReadAll(r)
	closeErr := r.Close()
	if readErr != nil || closeErr != nil {
		return "", false
	}
	return string(data), true
}

func (s *ChangesetService) writeDiffCache(ctx context.Context, key, diff string) {
	if err := s.ObjectStore.Put(ctx, key, bytes.NewReader([]byte(diff))); err != nil {
		slog.Warn("failed to cache changeset diff", "cache_key", key, "error", err)
	}
}
