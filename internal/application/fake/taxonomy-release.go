package fake

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func cloneRelease(record *taxonomy.ReleaseRecord) *taxonomy.ReleaseRecord {
	if record == nil {
		return nil
	}
	copy := *record
	copy.Previous = cloneTaxonomy(record.Previous)
	return &copy
}

func (r taxonomies) LatestRelease(context.Context) (*taxonomy.ReleaseRecord, error) {
	return cloneRelease(r.d.taxonomyRelease), nil
}

func (r taxonomies) RecordRelease(
	_ context.Context,
	release taxonomy.Release,
	version int64,
	previous taxonomy.Definition,
) error {
	r.d.taxonomyRelease = &taxonomy.ReleaseRecord{
		ReleaseState: taxonomy.ReleaseState{
			Version:          release.Version,
			Checksum:         release.Checksum,
			PublishedVersion: version,
		},
		Previous: cloneTaxonomy(previous),
	}
	return nil
}

func (r taxonomies) MarkRollback(_ context.Context, release, version int64) error {
	if r.d.taxonomyRelease == nil || r.d.taxonomyRelease.Version != release {
		return taxonomy.ErrReleaseConflict
	}
	r.d.taxonomyRelease.RolledBackVersion = version
	return nil
}
