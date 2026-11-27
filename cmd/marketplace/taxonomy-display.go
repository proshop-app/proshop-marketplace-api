package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/jobs"
)

// Карточки и варианты читают обновляемый снимок без обращения к базе из HTTP.
func (s *server) refreshTaxonomyDisplay(ctx context.Context, watch ports.WorkWatch) error {
	recovery := time.NewTicker(jobs.WorkRecovery)
	defer recovery.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-watch.Changed():
			watch.Take()
		case <-recovery.C:
		}
		if err := s.app.queries.RefreshTaxonomyDisplay(ctx, s.uow.TaxonomyDisplay); err != nil && ctx.Err() == nil {
			s.logger.Error("снимок отображения таксономии не обновлён", slog.Any("error", err))
		}
	}
}

func (s *server) refreshTaxonomySelection(ctx context.Context, watch ports.WorkWatch) error {
	refresh := s.app.taxonomy.NewSelectionRefresh()
	ticker := time.NewTicker(jobs.WorkRecovery)
	defer ticker.Stop()
	for {
		err := s.shortSchedules.Run(ctx, refresh.RunOnce)
		if err != nil && !errors.Is(err, ports.ErrIntervalTaken) && ctx.Err() == nil {
			s.logger.Error("снимок выбора из справочников не обновлён", slog.Any("error", err))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-watch.Changed():
			watch.Take()
		case <-ticker.C:
		}
	}
}
