package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

// Карточки и варианты читают обновляемый снимок без обращения к базе из HTTP.
func (s *server) refreshTaxonomyDisplay(ctx context.Context) error {
	ctx = ports.WithDatabaseBudget(ctx, ports.DatabaseBudget(s.cfg.Database.Background))
	ticker := time.NewTicker(taxonomyDisplayInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.app.queries.RefreshTaxonomyDisplay(ctx); err != nil && ctx.Err() == nil {
				s.logger.Error("снимок отображения таксономии не обновлён", slog.Any("error", err))
			}
		}
	}
}

func (s *server) refreshTaxonomySelection(ctx context.Context) error {
	ctx = ports.WithDatabaseBudget(ctx, ports.DatabaseBudget(s.cfg.Database.Background))
	refresh := s.app.taxonomy.NewSelectionRefresh()
	ticker := time.NewTicker(taxonomySelectionInterval)
	defer ticker.Stop()
	for {
		if err := refresh.RunOnce(ctx); err != nil && ctx.Err() == nil {
			s.logger.Error("снимок выбора из справочников не обновлён", slog.Any("error", err))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
