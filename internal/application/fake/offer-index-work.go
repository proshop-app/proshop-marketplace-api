package fake

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type offerIndexWork struct {
	d *data
}

func (t *tx) OfferIndexWork() ports.OfferIndexWork {
	return offerIndexWork{d: t.data}
}

func (r offerIndexWork) BeginRebuild(
	_ context.Context,
	generation string,
	started time.Time,
) (ports.IndexRebuildProgress, error) {
	if r.d.indexRebuild.Generation != generation {
		r.d.indexRebuild = ports.IndexRebuildProgress{Generation: generation, Started: started}
	}
	return r.d.indexRebuild, nil
}

func (r offerIndexWork) SaveRebuild(_ context.Context, from, to ports.IndexRebuildProgress) error {
	if from != r.d.indexRebuild || from.Complete {
		return ports.ErrOfferIndexProgressConflict
	}
	r.d.indexRebuild = to
	return nil
}

func (r offerIndexWork) TenantProgress(
	_ context.Context,
	tenant ids.TenantID,
) (ports.TenantIndexProgress, int64, error) {
	progress, exists := r.d.indexTenants[tenant]
	if !exists {
		progress.Complete = true
	}
	version := r.d.participants[tenant].Version + r.d.tenants[tenant].Version
	return progress, version, nil
}

func (r offerIndexWork) SaveTenant(_ context.Context, tenant ids.TenantID, progress ports.TenantIndexProgress) error {
	if progress.Revision != r.d.indexTenants[tenant].Revision {
		return ports.ErrOfferIndexProgressConflict
	}
	progress.Revision++
	r.d.indexTenants[tenant] = progress
	return nil
}
