package postgres

import (
	"context"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/proto/core/v1"
)

func (s *SliceStore) ListSorted(ctx context.Context, account, visibilityMode, subjectID, afterCursor string, limit int, includeArchived bool, sortOrder storage.SliceSort) ([]*corev1.Slice, string, error) {
	if sortOrder == "" || sortOrder == storage.SliceSortName {
		publicOnly := visibilityMode != "all"
		sharedWith := ""
		if visibilityMode == "visible" {
			sharedWith = strings.TrimSpace(subjectID)
		}
		if limit <= 0 || limit > 1000 {
			limit = 50
		}
		slices, err := s.list(ctx, account, afterCursor, limit+1, publicOnly, sharedWith, includeArchived)
		if err != nil {
			return nil, "", err
		}
		var next string
		if len(slices) > limit {
			slices = slices[:limit]
			next = slices[limit-1].GetRef().GetSlice()
		}
		return slices, next, nil
	}
	return s.listSortedKeyset(ctx, account, visibilityMode, subjectID, afterCursor, limit, includeArchived, sortOrder)
}

func (s *SliceStore) listSortedKeyset(ctx context.Context, account, visibilityMode, subjectID, afterCursor string, limit int, includeArchived bool, sortOrder storage.SliceSort) ([]*corev1.Slice, string, error) {
	account, err := storage.NormalizeSlug(account, "account")
	if err != nil {
		return nil, "", err
	}
	if limit <= 0 || limit > 1000 {
		limit = 50
	}
	cur, hasCur, err := storage.DecodeSliceCursor(afterCursor)
	if err != nil {
		return nil, "", err
	}
	publicOnly := visibilityMode != "all"
	sharedWith := ""
	if visibilityMode == "visible" {
		sharedWith = strings.TrimSpace(subjectID)
	}
	sortTsCol := "slices.created_at"
	if sortOrder == storage.SliceSortActive {
		sortTsCol = "act.active_at"
	}
	query := `
		select ` + sliceSelectColumns + `,
		       slices.created_at,
		       act.active_at,
		       act.sort_is_home,
		       ` + sortTsCol + `
		from slices
		join accounts on accounts.id = slices.account_id
		cross join lateral (
			select coalesce((
				select max(c.updated_at)
				from changesets c
				where c.authoring_slice_id = slices.id and c.status = 'submitted'
			), slices.created_at) as active_at,
			(slices.slug = 'home') as sort_is_home
		) act
		where accounts.slug = $1 and slices.deleted_at is null
		  and ($4 or slices.archived_at is null)
		  and (not $2 or slices.visibility = 'public' or ($3 <> '' and (
			exists (select 1 from slice_members sm where sm.slice_id = slices.id and sm.subject_id = $3)
			or exists (
				select 1
				from slice_group_members sgm
				join account_group_members agm on agm.group_id = sgm.group_id
				where sgm.slice_id = slices.id and agm.subject_id = $3
			)
		  )))
		  and (
			not $5
			or (act.sort_is_home < $6)
			or (act.sort_is_home = $6 and ` + sortTsCol + ` < $7)
			or (act.sort_is_home = $6 and ` + sortTsCol + ` = $7 and slices.slug > $8)
		  )
		order by act.sort_is_home desc, ` + sortTsCol + ` desc, slices.slug asc
		limit $9
	`
	rows, err := s.db.QueryContext(ctx, query, account, publicOnly, sharedWith, includeArchived, hasCur, cur.IsHome, cur.Ts, cur.Slug, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	type rowMeta struct {
		isHome bool
		sortTs time.Time
		slug   string
	}
	var out []*corev1.Slice
	var metas []rowMeta
	for rows.Next() {
		var createdAt, activeAt, sortTs time.Time
		var isHome bool
		slice, err := scanSlice(metaScanner{row: rows, dest: []any{&createdAt, &activeAt, &isHome, &sortTs}})
		if err != nil {
			return nil, "", err
		}
		slice.CreatedAt = formatTime(createdAt)
		slice.LastActivityAt = formatTime(activeAt)
		out = append(out, slice)
		metas = append(metas, rowMeta{isHome: isHome, sortTs: sortTs, slug: slice.GetRef().GetSlice()})
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	var next string
	if len(out) > limit {
		out = out[:limit]
		last := metas[limit-1]
		next = storage.EncodeSliceCursor(last.isHome, last.sortTs, last.slug)
	}
	return out, next, nil
}

type metaScanner struct {
	row  scanner
	dest []any
}

func (m metaScanner) Scan(dest ...any) error {
	dest = append(dest, m.dest...)
	return m.row.Scan(dest...)
}
