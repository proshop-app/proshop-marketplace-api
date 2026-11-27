package main

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/searchapi"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/jobs"
)

// workJobs связывает записи после commit, сигналы и циклы, ожидающие работу.
func (s *server) workJobs() []jobs.Loop {
	now := clock.System{}
	outbox := jobs.NewOutboxRunner(s.logger, s.metrics, now)
	indexLane := messaging.NewIndependentLane(
		s.app.dispatcher,
		ports.TaskLaneGate{
			Key:   "offer-index",
			Kinds: searchLaneKinds,
		},
		30*time.Second,
		func(err error) bool { return errors.Is(err, searchapi.ErrUnavailable) },
		s.store.index.ConfirmOutage,
	)
	background := postgres.NewTaskQueue(s.store.pool(ports.DatabaseBackground))
	orders := postgres.NewTaskQueue(s.store.pool(ports.DatabaseOrders))
	creation := jobs.NewCreationRunner(s.app.creation, s.logger, outbox.RecordOutcome, now)
	creationKinds := s.app.creation.WorkKinds()
	creationWatch := s.wake.Watch(func(kind string) bool { return slices.Contains(creationKinds, kind) })
	excluded := s.app.dispatcher.WorkRequest().ExcludeKinds
	outboxWatch := s.wake.Watch(func(kind string) bool {
		switch kind {
		case ports.TaxonomyWork, ports.TaxonomyDisplayWork, ports.TaxonomySelectionWork, ports.IndexMaintenanceWork:
			return false
		default:
			return !slices.Contains(excluded, kind)
		}
	})
	taxonomyWatch := s.wake.Watch(func(kind string) bool { return kind == ports.TaxonomyWork })
	displayWatch := s.wake.Watch(func(kind string) bool { return kind == ports.TaxonomyDisplayWork })
	indexInterval := s.cfg.Search.MaintenanceInterval
	indexWatch := s.wake.Watch(func(kind string) bool { return kind == ports.IndexMaintenanceWork })
	// Допуск берётся через пул. Пересборка берёт один общий замок внутри Maintain.
	selectionWatch := s.wake.Watch(func(kind string) bool { return kind == ports.TaxonomySelectionWork })
	loops := []jobs.Loop{
		{Name: "taxonomy-selection", Run: func(ctx context.Context) error {
			return s.refreshTaxonomySelection(ctx, selectionWatch)
		}},
		{Name: "taxonomy-display", Run: func(ctx context.Context) error {
			return s.refreshTaxonomyDisplay(ctx, displayWatch)
		}},
		{Name: "taxonomy-revalidation", Run: func(ctx context.Context) error {
			return jobs.RunTaxonomyWork(ctx, s.app.taxonomy, background.NextTaxonomyWork, taxonomyWatch, s.logger, now)
		}},
		{Name: "index-maintenance", Run: jobs.NewIndexMaintenanceRunner(
			s.app.indexer,
			indexInterval,
			s.logger,
			s.metrics,
		).WithSchedule(s.shortSchedules, indexWatch).Run},
		{Name: "checkout-creation", Run: func(ctx context.Context) error {
			return creation.Run(ctx, orders, creationWatch)
		}},
		{Name: "outbox", Run: func(ctx context.Context) error {
			return outbox.RunIndependent(ctx, indexLane, background, outboxWatch)
		}},
	}
	return loops
}
