package postgres

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/objectid"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type pathEntity struct {
	Path        string
	AccountID   string
	EntityID    string
	Kind        string
	ContentHash string
	Mode        uint32
}

type entityChange struct {
	Entity      pathEntity
	Path        string
	OldPath     string
	ChangeKind  string
	Source      string
	Confidence  int
	ContentHash string
	Mode        uint32
}

func (s *ChangesetStore) applyEntityHistoryTx(ctx context.Context, tx *sql.Tx, targetRef, baseCommitID, commitID string, edits []*corev1.FileEdit, committedAt time.Time) error {
	stage, err := s.newEntityHistoryStageTx(ctx, tx, targetRef, baseCommitID, edits)
	if err != nil {
		return err
	}
	inferredMoves, err := s.inferExactMovesTx(ctx, tx, stage, edits)
	if err != nil {
		return err
	}
	matchedOld := map[string]struct{}{}
	matchedNew := map[string]struct{}{}
	for _, move := range inferredMoves {
		matchedOld[move.OldPath] = struct{}{}
		matchedNew[move.Path] = struct{}{}
		if err := stage.moveEntity(ctx, tx, move.Entity, move.OldPath, move.Path, commitID, committedAt, "exact_content_match", move.ContentHash, move.Mode); err != nil {
			return err
		}
	}
	for _, edit := range edits {
		switch edit.Op {
		case "rename":
			entity, err := stage.ensurePathEntity(ctx, tx, edit.OldPath)
			if err != nil {
				return err
			}
			if entity == nil {
				return ErrConflict
			}
			if err := stage.moveEntity(ctx, tx, *entity, edit.OldPath, edit.Path, commitID, committedAt, "explicit", entity.ContentHash, entity.Mode); err != nil {
				return err
			}
		case "copy":
			// The copy is a new entity, added from the source, and so is every
			// path under a copied folder: the flat index serves reads at the
			// head, so it holds them all.
			source, err := stage.ensurePathEntity(ctx, tx, edit.OldPath)
			if err != nil {
				return err
			}
			if source == nil {
				return ErrConflict
			}
			accountID, err := stage.accountIDForPath(ctx, tx, edit.Path)
			if err != nil {
				return err
			}
			if err := stage.flushPathState(ctx, tx); err != nil {
				return err
			}
			if err := copyCurrentPathEntitiesTx(ctx, tx, stage.targetRef, edit.OldPath, edit.Path, accountID, commitID); err != nil {
				return err
			}
			if err := stage.reloadCurrentPathEntities(ctx, tx); err != nil {
				return err
			}
			created, ok := stage.current[edit.Path]
			if !ok {
				return ErrConflict
			}
			stage.insertEntityChange(entityChange{Entity: created, Path: edit.Path, OldPath: edit.OldPath, ChangeKind: "added", Source: "copy", Confidence: 100, ContentHash: created.ContentHash, Mode: created.Mode})
		case "mkdir":
			entity, err := stage.ensurePathEntity(ctx, tx, edit.Path)
			if err != nil {
				return err
			}
			if entity != nil {
				continue
			}
			created, err := stage.createEntity(ctx, tx, commitID, edit.Path, "directory", "", 0)
			if err != nil {
				return err
			}
			stage.upsertCurrentPathEntity(*created)
			stage.insertEntityChange(entityChange{Entity: *created, Path: edit.Path, ChangeKind: "added", Source: "explicit", Confidence: 100})
		case "delete":
			if _, ok := matchedOld[edit.Path]; ok {
				continue
			}
			entity, err := stage.ensurePathEntity(ctx, tx, edit.Path)
			if err != nil {
				return err
			}
			if entity == nil {
				continue
			}
			if err := stage.deleteCurrentPathEntity(ctx, tx, edit.Path, entity.Kind == "directory"); err != nil {
				return err
			}
			stage.markEntityDeleted(*entity, commitID)
			stage.insertEntityChange(entityChange{Entity: *entity, Path: edit.Path, ChangeKind: "deleted", Source: "explicit", Confidence: 100, ContentHash: entity.ContentHash, Mode: entity.Mode})
		default:
			if _, ok := matchedNew[edit.Path]; ok {
				continue
			}
			entity, err := stage.ensurePathEntity(ctx, tx, edit.Path)
			if err != nil {
				return err
			}
			changeKind := "modified"
			var current pathEntity
			if entity == nil {
				changeKind = "added"
				created, err := stage.createEntity(ctx, tx, commitID, edit.Path, "file", edit.ContentHash, edit.Mode)
				if err != nil {
					return err
				}
				current = *created
			} else {
				current = *entity
				current.ContentHash = edit.ContentHash
				current.Mode = edit.Mode
			}
			stage.upsertCurrentPathEntity(current)
			stage.insertEntityChange(entityChange{Entity: current, Path: edit.Path, ChangeKind: changeKind, Source: "explicit", Confidence: 100, ContentHash: edit.ContentHash, Mode: edit.Mode})
		}
	}
	return stage.flush(ctx, tx, commitID, committedAt)
}

type entityHistoryStage struct {
	store        *ChangesetStore
	targetRef    string
	baseCommitID string
	paths        []string

	current       map[string]pathEntity
	dirtyUpserts  map[string]pathEntity
	dirtyDeletes  map[string]struct{}
	created       []stagedEntityCreation
	deleted       []stagedEntityDeletion
	changes       []entityChange
	accountIDs    map[string]string
	accountsReady bool
}

type stagedEntityCreation struct {
	entity          pathEntity
	createdCommitID string
}

type stagedEntityDeletion struct {
	entity   pathEntity
	commitID string
}

func (s *ChangesetStore) newEntityHistoryStageTx(ctx context.Context, tx *sql.Tx, targetRef, baseCommitID string, edits []*corev1.FileEdit) (*entityHistoryStage, error) {
	paths := entityHistoryPaths(edits)
	current, err := getCurrentPathEntitiesTx(ctx, tx, targetRef, paths)
	if err != nil {
		return nil, err
	}
	return &entityHistoryStage{
		store:        s,
		targetRef:    targetRef,
		baseCommitID: baseCommitID,
		paths:        paths,
		current:      current,
		dirtyUpserts: map[string]pathEntity{},
		dirtyDeletes: map[string]struct{}{},
		accountIDs:   map[string]string{},
	}, nil
}

func entityHistoryPaths(edits []*corev1.FileEdit) []string {
	seen := map[string]struct{}{}
	for _, edit := range edits {
		if edit == nil {
			continue
		}
		if edit.Path != "" {
			seen[edit.Path] = struct{}{}
		}
		if edit.OldPath != "" {
			seen[edit.OldPath] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func (stage *entityHistoryStage) ensurePathEntity(ctx context.Context, tx *sql.Tx, p string) (*pathEntity, error) {
	if entity, ok := stage.current[p]; ok {
		return &entity, nil
	}
	entry, err := stage.store.repository.getEntryAtCommitTx(ctx, tx, stage.baseCommitID, p)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	kind := entry.Kind
	contentHash := ""
	mode := uint32(0)
	if kind == "file" {
		contentHash = entry.ContentHash
		mode = entry.Mode
	}
	entity, err := stage.createEntity(ctx, tx, "", p, kind, contentHash, mode)
	if err != nil {
		return nil, err
	}
	stage.upsertCurrentPathEntity(*entity)
	return entity, nil
}

func (stage *entityHistoryStage) createEntity(ctx context.Context, tx *sql.Tx, commitID, p, kind, contentHash string, mode uint32) (*pathEntity, error) {
	accountID, err := stage.accountIDForPath(ctx, tx, p)
	if err != nil {
		return nil, err
	}
	entityID, err := objectid.RandomID("ent")
	if err != nil {
		return nil, err
	}
	entity := pathEntity{Path: p, AccountID: accountID, EntityID: entityID, Kind: kind, ContentHash: contentHash, Mode: mode}
	stage.created = append(stage.created, stagedEntityCreation{entity: entity, createdCommitID: commitID})
	return &entity, nil
}

func (stage *entityHistoryStage) accountIDForPath(ctx context.Context, tx *sql.Tx, p string) (string, error) {
	if !stage.accountsReady {
		ids, err := accountIDsForPathsTx(ctx, tx, stage.paths)
		if err != nil {
			return "", err
		}
		stage.accountIDs = ids
		stage.accountsReady = true
	}
	trimmed := strings.Trim(p, "/")
	if trimmed == "" {
		return "", ErrInvalid
	}
	slug := strings.Split(trimmed, "/")[0]
	accountID, ok := stage.accountIDs[slug]
	if !ok {
		return "", ErrNotFound
	}
	return accountID, nil
}

func (stage *entityHistoryStage) upsertCurrentPathEntity(entity pathEntity) {
	stage.current[entity.Path] = entity
	stage.dirtyUpserts[entity.Path] = entity
	delete(stage.dirtyDeletes, entity.Path)
}

func (stage *entityHistoryStage) deleteCurrentPathEntity(ctx context.Context, tx *sql.Tx, p string, recursive bool) error {
	if recursive {
		if err := stage.flushPathState(ctx, tx); err != nil {
			return err
		}
		if err := deleteCurrentPathEntityTx(ctx, tx, stage.targetRef, p, true); err != nil {
			return err
		}
		return stage.reloadCurrentPathEntities(ctx, tx)
	}
	delete(stage.current, p)
	delete(stage.dirtyUpserts, p)
	stage.dirtyDeletes[p] = struct{}{}
	return nil
}

func (stage *entityHistoryStage) moveCurrentPathEntity(ctx context.Context, tx *sql.Tx, oldPath, newPath string, entity pathEntity) error {
	if entity.Kind == "directory" {
		if err := stage.flushPathState(ctx, tx); err != nil {
			return err
		}
		if err := moveCurrentPathEntityTx(ctx, tx, stage.targetRef, oldPath, newPath, entity); err != nil {
			return err
		}
		return stage.reloadCurrentPathEntities(ctx, tx)
	}
	if err := stage.deleteCurrentPathEntity(ctx, tx, oldPath, false); err != nil {
		return err
	}
	stage.upsertCurrentPathEntity(entity)
	return nil
}

func (stage *entityHistoryStage) moveEntity(ctx context.Context, tx *sql.Tx, entity pathEntity, oldPath, newPath, commitID string, committedAt time.Time, source, contentHash string, mode uint32) error {
	entity.Path = newPath
	if contentHash != "" {
		entity.ContentHash = contentHash
	}
	if mode != 0 {
		entity.Mode = mode
	}
	if err := stage.moveCurrentPathEntity(ctx, tx, oldPath, newPath, entity); err != nil {
		return err
	}
	stage.insertEntityChange(entityChange{
		Entity:      entity,
		Path:        newPath,
		OldPath:     oldPath,
		ChangeKind:  "moved",
		Source:      source,
		Confidence:  100,
		ContentHash: entity.ContentHash,
		Mode:        entity.Mode,
	})
	return nil
}

func (stage *entityHistoryStage) insertEntityChange(change entityChange) {
	stage.changes = append(stage.changes, change)
}

func (stage *entityHistoryStage) markEntityDeleted(entity pathEntity, commitID string) {
	stage.deleted = append(stage.deleted, stagedEntityDeletion{entity: entity, commitID: commitID})
}

func (stage *entityHistoryStage) reloadCurrentPathEntities(ctx context.Context, tx *sql.Tx) error {
	current, err := getCurrentPathEntitiesTx(ctx, tx, stage.targetRef, stage.paths)
	if err != nil {
		return err
	}
	stage.current = current
	stage.dirtyUpserts = map[string]pathEntity{}
	stage.dirtyDeletes = map[string]struct{}{}
	return nil
}

func (stage *entityHistoryStage) flush(ctx context.Context, tx *sql.Tx, commitID string, committedAt time.Time) error {
	if err := stage.flushPathState(ctx, tx); err != nil {
		return err
	}
	if err := insertEntityChangesTx(ctx, tx, stage.targetRef, commitID, stage.changes, committedAt); err != nil {
		return err
	}
	return markEntitiesDeletedTx(ctx, tx, stage.deleted)
}

func (stage *entityHistoryStage) flushPathState(ctx context.Context, tx *sql.Tx) error {
	if err := insertFSEntitiesTx(ctx, tx, stage.created); err != nil {
		return err
	}
	stage.created = nil
	if err := upsertCurrentPathEntitiesTx(ctx, tx, stage.targetRef, pathEntitiesFromMap(stage.dirtyUpserts)); err != nil {
		return err
	}
	if err := deleteCurrentPathEntitiesTx(ctx, tx, stage.targetRef, stringsFromSet(stage.dirtyDeletes)); err != nil {
		return err
	}
	stage.dirtyUpserts = map[string]pathEntity{}
	stage.dirtyDeletes = map[string]struct{}{}
	return nil
}
