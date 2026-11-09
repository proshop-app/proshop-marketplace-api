package taxonomy

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/moderationstatus"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

// TaskRevalidate — один проход каталога по опубликованной версии таксономии.
const TaskRevalidate = "taxonomy.products.revalidate"

type revalidationPayload struct {
	Version int64 `json:"version"`
}

func (s *Service) schedule(ctx context.Context, tx ports.Tx, version int64) error {
	progress, err := tx.TaxonomyRevalidation().LockProgress(ctx)
	if err != nil {
		return err
	}
	// Сторонние задачи могли построить сводки по прежней голове ещё до первой пачки.
	if err := tx.TaxonomyRevalidation().SaveProgress(ctx, ports.TaxonomyProgress{
		Version:      version,
		CardsVersion: progress.CardsVersion,
		CardsDirty:   progress.CardsDirty || !progress.Done,
	}); err != nil {
		return err
	}
	return s.enqueueRevalidation(ctx, tx, version)
}

func (s *Service) enqueueRevalidation(ctx context.Context, tx ports.Tx, version int64) error {
	payload, _ := json.Marshal(revalidationPayload{Version: version})
	return tx.Outbox().Enqueue(ctx, ports.NewTask{
		Kind:           TaskRevalidate,
		DedupKey:       TaskRevalidate,
		ReplacePayload: true,
		Payload:        payload,
		At:             s.clock.Now(),
	})
}

// HandleTask выполняет одну пачку; незавершённый проход ставит продолжение
// в той же транзакции, что и курсор.
func (s *Service) HandleTask(ctx context.Context, task ports.Task) error {
	var payload revalidationPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return taxonomy.ErrInvalid.Wrap(err)
	}
	if task.Kind != TaskRevalidate || task.TenantID != "" || payload.Version <= 0 {
		return taxonomy.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	err := s.revalidateBatch(ctx, payload.Version)
	if err != nil && ctx.Err() == nil && !errors.Is(err, context.Canceled) {
		slog.ErrorContext(ctx, "перепроверка таксономии прервана", "version", payload.Version, "error", err)
	}
	return err
}

func (s *Service) revalidateBatch(ctx context.Context, version int64) error {
	return s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		definition, err := tx.Taxonomy().Current(ctx)
		if err != nil {
			return err
		}
		progress, err := tx.TaxonomyRevalidation().LockProgress(ctx)
		if err != nil {
			return err
		}
		if version < definition.Version {
			return nil
		}
		if version != definition.Version || progress.Version != version {
			return taxonomy.ErrConflict
		}
		if progress.Done {
			return nil
		}
		items, err := tx.Products().Page(ctx, progress.TenantID, progress.ProductID, revalidationBatch)
		if err != nil {
			return err
		}
		old, err := s.snapshot(ctx, tx, progress.CardsVersion)
		if err != nil {
			return err
		}
		refreshStores := progress.CardsDirty || treeNeedsRefresh(old.Tree, categoryTree(definition.Categories))
		lastStore := string(progress.TenantID)
		for _, p := range items {
			if err := s.revalidateWithRetry(ctx, tx, p, definition); err != nil {
				return err
			}
			state := p.State()
			if refreshStores && lastStore != string(state.TenantID) {
				lastStore = string(state.TenantID)
				task := ports.StoreCardTask(state.TenantID, s.clock.Now().Add(s.refreshDelay))
				if err := tx.Outbox().Enqueue(ctx, task); err != nil {
					return err
				}
			}
			progress.TenantID, progress.ProductID = state.TenantID, state.ProductID
		}
		progress.Done = len(items) < revalidationBatch
		if progress.Done {
			progress.CardsVersion = version
			progress.CardsDirty = false
		}
		done := progress.Done
		if err := tx.TaxonomyRevalidation().SaveProgress(ctx, progress); err != nil {
			return err
		}
		if !done {
			if err := s.enqueueRevalidation(ctx, tx, version); err != nil {
				return err
			}
		}
		tx.AfterCommit(func() {
			slog.InfoContext(
				ctx,
				"пачка перепроверки таксономии",
				"version",
				version,
				"tenantId",
				progress.TenantID,
				"productId",
				progress.ProductID,
				"count",
				len(items),
				"done",
				done,
			)
		})
		return nil
	})
}

func (s *Service) revalidateProduct(
	ctx context.Context,
	tx ports.Tx,
	p *catalog.Product,
	definition taxonomy.Definition,
) error {
	state := p.State()
	previous := state.Card.Publication
	if state.Deleted || previous.Version >= definition.Version {
		return nil
	}
	if !p.Revalidate(definition, s.clock.Now()) {
		return nil
	}
	next := p.State().Card.Publication
	policyVersion := previous.Version
	verdict, err := tx.ModerationVerdicts().Get(ctx, state.TenantID, state.ProductID)
	if err != nil && !errors.Is(err, moderation.ErrNotFound) {
		return err
	}
	operatorDecision := false
	if err == nil {
		v := verdict.State()
		policyVersion = v.TaxonomyVersion
		operatorDecision = v.DecidedBy == moderation.Operator && v.Revision == state.Revision
	}
	policyChanged := false
	if !operatorDecision && previous.CategoryID != "" {
		old, err := s.snapshot(ctx, tx, policyVersion)
		if err != nil {
			return err
		}
		policyChanged = old.policy(previous.CategoryID) != definition.ModerationPolicy(next.CategoryID)
	}
	// Номер проверки сам по себе не меняет публикацию. Для таких товаров прогресс хранит курсор.
	previous.Version = next.Version
	if reflect.DeepEqual(previous, next) && !policyChanged {
		return nil
	}
	publicationChanged := previous.Published != next.Published || policyChanged ||
		!reflect.DeepEqual(previous.Issues, next.Issues)
	// Путь, полное имя категории и проверенные атрибуты также лежат в Publication и документе индекса.
	if err := tx.Products().Save(ctx, p); err != nil {
		return err
	}
	if publicationChanged {
		task := moderationstatus.OfferTask(state.TenantID, state.ProductID, s.clock.Now())
		if err := tx.Outbox().Enqueue(ctx, task); err != nil {
			return err
		}
		if s.evaluator != nil && !operatorDecision {
			if err := s.evaluateWithRetry(ctx, tx, p); err != nil {
				return err
			}
		}
	}
	return tx.Outbox().Enqueue(ctx, appcatalog.SyncOfferTask(state.TenantID, state.ProductID, s.clock.Now()))
}

func (s *Service) revalidateWithRetry(
	ctx context.Context,
	tx ports.Tx,
	p *catalog.Product,
	definition taxonomy.Definition,
) error {
	// Условный UPDATE при конфликте не портит транзакцию. Перечитываем и проверяем
	// заново: refreshModelKeys мог сохранить товар без проверки таксономии.
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := s.revalidateProduct(ctx, tx, p, definition)
		if !errors.Is(err, catalog.ErrConflict) {
			return err
		}
		state := p.State()
		p, err = tx.Products().Get(ctx, state.TenantID, state.ProductID)
		if err != nil {
			return err
		}
	}
}

// Конфликт вердикта не отменяет уже сохранённую публикацию: Evaluate перечитывает
// вердикт при каждом повторе в той же транзакции.
func (s *Service) evaluateWithRetry(ctx context.Context, tx ports.Tx, p *catalog.Product) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := s.evaluator.Evaluate(ctx, tx, p, nil, s.clock.Now())
		if !errors.Is(err, moderation.ErrConflict) {
			return err
		}
	}
}
