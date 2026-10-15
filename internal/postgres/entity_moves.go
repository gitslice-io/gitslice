package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type inferredMove struct {
	OldPath     string
	Path        string
	Entity      pathEntity
	ContentHash string
	Mode        uint32
}

func (s *ChangesetStore) inferExactMovesTx(ctx context.Context, tx *sql.Tx, stage *entityHistoryStage, edits []*corev1.FileEdit) ([]inferredMove, error) {
	type deleteCandidate struct {
		path   string
		entity pathEntity
	}
	type upsertCandidate struct {
		path        string
		contentHash string
		mode        uint32
	}
	deletesByKey := map[string][]deleteCandidate{}
	upsertsByKey := map[string][]upsertCandidate{}
	for _, edit := range edits {
		switch edit.Op {
		case "delete":
			entity, err := stage.ensurePathEntity(ctx, tx, edit.Path)
			if err != nil {
				return nil, err
			}
			if entity == nil || entity.Kind != "file" || entity.ContentHash == "" {
				continue
			}
			key := exactMoveKey(entity.ContentHash, entity.Mode)
			deletesByKey[key] = append(deletesByKey[key], deleteCandidate{path: edit.Path, entity: *entity})
		case "upsert", "add", "update":
			if edit.ContentHash == "" {
				continue
			}
			entity, err := stage.ensurePathEntity(ctx, tx, edit.Path)
			if err != nil {
				return nil, err
			}
			if entity != nil {
				continue
			}
			mode := edit.Mode
			if mode == 0 {
				mode = 0o100644
			}
			key := exactMoveKey(edit.ContentHash, mode)
			upsertsByKey[key] = append(upsertsByKey[key], upsertCandidate{path: edit.Path, contentHash: edit.ContentHash, mode: mode})
		}
	}
	var out []inferredMove
	for key, deletes := range deletesByKey {
		upserts := upsertsByKey[key]
		if len(deletes) != 1 || len(upserts) != 1 {
			continue
		}
		out = append(out, inferredMove{
			OldPath:     deletes[0].path,
			Path:        upserts[0].path,
			Entity:      deletes[0].entity,
			ContentHash: upserts[0].contentHash,
			Mode:        upserts[0].mode,
		})
	}
	return out, nil
}

func exactMoveKey(contentHash string, mode uint32) string {
	return contentHash + "\x00" + fmt.Sprint(mode)
}

func (s *ChangesetStore) moveEntityTx(ctx context.Context, tx *sql.Tx, targetRef string, entity pathEntity, oldPath, newPath, commitID string, committedAt time.Time, source, contentHash string, mode uint32) error {
	entity.Path = newPath
	if contentHash != "" {
		entity.ContentHash = contentHash
	}
	if mode != 0 {
		entity.Mode = mode
	}
	if err := moveCurrentPathEntityTx(ctx, tx, targetRef, oldPath, newPath, entity); err != nil {
		return err
	}
	return insertEntityChangeTx(ctx, tx, targetRef, commitID, entityChange{
		Entity:      entity,
		Path:        newPath,
		OldPath:     oldPath,
		ChangeKind:  "moved",
		Source:      source,
		Confidence:  100,
		ContentHash: entity.ContentHash,
		Mode:        entity.Mode,
	}, committedAt)
}
