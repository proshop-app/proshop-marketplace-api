package fake

import (
	"context"
	"slices"
	"sort"

	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type snapshotVersion struct {
	row      reference.SnapshotRow
	revision int64
}

func (r references) SnapshotChanges(
	_ context.Context,
	dictionary string,
	since, revision, after int64,
	limit int,
) ([]reference.SnapshotRow, error) {
	return r.snapshotPage(dictionary, revision, after, limit, func(version snapshotVersion) bool {
		return version.revision > since
	}), nil
}

func (r references) SnapshotEntries(
	_ context.Context,
	dictionary string,
	revision, after int64,
	limit int,
) ([]reference.SnapshotRow, error) {
	return r.snapshotPage(dictionary, revision, after, limit, func(version snapshotVersion) bool {
		return version.row.State != nil && version.row.State.Status == reference.StatusConfirmed
	}), nil
}

func (r references) snapshotPage(
	dictionary string,
	revision, after int64,
	limit int,
	matches func(snapshotVersion) bool,
) []reference.SnapshotRow {
	versions := map[ids.UUID]snapshotVersion{}
	var position int64
	for _, change := range r.d.reference.changes {
		for _, image := range change.Forward {
			position++
			if change.Dictionary != dictionary || change.Revision > revision {
				continue
			}
			versions[image.ID] = snapshotVersion{
				row: reference.SnapshotRow{
					ID:       image.ID,
					Position: position,
					State:    image.State,
				},
				revision: change.Revision,
			}
		}
	}

	var page []snapshotVersion
	for _, version := range versions {
		if version.row.Position > after && matches(version) {
			page = append(page, version)
		}
	}
	sort.Slice(page, func(i, j int) bool { return page[i].row.Position < page[j].row.Position })

	// Копии нужны только для записей, которые вышли из хранилища на этой странице.
	result := make([]reference.SnapshotRow, 0, min(len(page), limit))
	for _, version := range page[:min(len(page), limit)] {
		row := version.row
		if row.State != nil {
			state := *row.State
			state.Revision = version.revision
			state.Metadata = slices.Clone(state.Metadata)
			state.Aliases = slices.Clone(state.Aliases)
			row.State = &state
		}
		result = append(result, row)
	}
	return result
}
