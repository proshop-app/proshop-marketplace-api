package fake

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func (r taxonomies) QueueRevalidation(_ context.Context, version int64, categories []string) error {
	for _, category := range categories {
		for i, job := range r.d.revalidations {
			if job.Category == category && job.Version < version {
				r.d.revalidations[i].Done = true
			}
		}
		r.d.revalidations = append(r.d.revalidations, taxonomy.Revalidation{
			Version:     version,
			Category:    category,
			AvailableAt: r.d.databaseNow(),
		})
	}
	return nil
}

func (r taxonomies) NextRevalidation(_ context.Context) (*taxonomy.Revalidation, error) {
	now := r.d.databaseNow()
	selected := -1
	for i, job := range r.d.revalidations {
		if job.Done || job.AvailableAt.After(now) {
			continue
		}
		if selected < 0 || earlierRevalidation(job, r.d.revalidations[selected]) {
			selected = i
		}
	}
	if selected < 0 {
		return nil, nil
	}
	job := r.d.revalidations[selected]
	job.Lease++
	job.AvailableAt = now.Add(time.Second)
	r.d.revalidations[selected] = job
	return &job, nil
}

func earlierRevalidation(a, b taxonomy.Revalidation) bool {
	if !a.AvailableAt.Equal(b.AvailableAt) {
		return a.AvailableAt.Before(b.AvailableAt)
	}
	return a.Version < b.Version || a.Version == b.Version && a.Category < b.Category
}

func (r taxonomies) LockRevalidation(_ context.Context, claimed taxonomy.Revalidation) (*taxonomy.Revalidation, error) {
	for _, job := range r.d.revalidations {
		if job.Version == claimed.Version && job.Category == claimed.Category &&
			job.Lease == claimed.Lease && !job.Done {
			return &job, nil
		}
	}
	return nil, nil
}

func (r taxonomies) AdvanceRevalidation(_ context.Context, job taxonomy.Revalidation) error {
	for i, existing := range r.d.revalidations {
		if existing.Version == job.Version && existing.Category == job.Category {
			job.AvailableAt = r.d.databaseNow()
			r.d.revalidations[i] = job
			return nil
		}
	}
	return taxonomy.ErrConflict
}
