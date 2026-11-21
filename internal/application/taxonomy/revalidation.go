package taxonomy

import (
	"context"
	"fmt"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/moderationstatus"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

var errBatchEvaluator = errs.New(errs.Internal, "taxonomy.batch_evaluator", "нет пакетной оценки модерации")

// RevalidateNext сначала фиксирует короткую аренду. Ошибка страницы оставляет её
// отложенной, поэтому следующий проход может взять другую категорию или версию.
func (s *Service) RevalidateNext(ctx context.Context) (*taxonomy.Revalidation, error) {
	var claimed *taxonomy.Revalidation
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		claimed, err = tx.Taxonomy().NextRevalidation(ctx)
		return err
	})
	if err != nil || claimed == nil {
		return nil, err
	}
	var result *taxonomy.Revalidation
	err = s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		definition, err := tx.Taxonomy().Current(ctx)
		if err != nil {
			return err
		}
		// Единый порядок с публикацией: голова → задание → товары по ключу.
		job, err := tx.Taxonomy().LockRevalidation(ctx, *claimed)
		if err != nil || job == nil {
			return err
		}
		result = job
		return s.revalidatePage(ctx, tx, job, definition)
	})
	if err != nil {
		return nil, fmt.Errorf("перепроверка %d/%s: %w", claimed.Version, claimed.Category, err)
	}
	return result, nil
}

func (s *Service) revalidatePage(
	ctx context.Context,
	tx ports.Tx,
	job *taxonomy.Revalidation,
	definition taxonomy.Definition,
) error {
	items, err := tx.Revalidation().Page(ctx, *job, revalidationBatch)
	if err != nil {
		return err
	}
	changes, failures, err := s.planRevalidation(ctx, tx, items, definition, job.Category)
	if err != nil {
		return err
	}
	failed, err := tx.Revalidation().Save(ctx, changes)
	if err != nil {
		return err
	}
	failures = append(failures, failed...)
	for i := range failures {
		failures[i].TaxonomyVersion = definition.Version
		failures[i].At = s.clock.Now()
		failures[i].Diagnostic = revalidationDiagnostic(failures[i].Diagnostic)
	}
	if err := tx.Revalidation().RecordFailures(ctx, failures); err != nil {
		return err
	}
	observeRevalidation(tx, changes, failures)
	if len(items) > 0 {
		last := items[len(items)-1].Key
		job.TenantCursor, job.ProductCursor = string(last.Tenant), string(last.Product)
	}
	job.Processed += int64(len(items))
	job.Done = len(items) < revalidationBatch
	return tx.Taxonomy().AdvanceRevalidation(ctx, *job)
}

func revalidationDiagnostic(message string) string {
	count := 0
	for offset := range message {
		if count == ports.RevalidationDiagnosticLimit {
			return message[:offset]
		}
		count++
	}
	return message
}

func (s *Service) planRevalidation(
	ctx context.Context,
	tx ports.Tx,
	items []ports.RevalidationItem,
	definition taxonomy.Definition,
	category string,
) ([]ports.RevalidationChange, []ports.RevalidationFailure, error) {
	if len(items) == 0 {
		return nil, nil, nil
	}
	checker := definition.CategoryChecker(category)
	now := s.clock.Now()
	products, failures := selectRevalidationProducts(items, definition.Version, checker, now)
	changes, err := s.evaluateRevalidation(ctx, tx, products, definition, checker)
	if err != nil {
		return nil, nil, err
	}
	ready, rejected := prepareRevalidationTasks(changes, now)
	return ready, append(failures, rejected...), nil
}

func selectRevalidationProducts(
	items []ports.RevalidationItem,
	version int64,
	checker taxonomy.Checker,
	now time.Time,
) ([]*catalog.Product, []ports.RevalidationFailure) {
	products := []*catalog.Product{}
	failures := []ports.RevalidationFailure{}
	for _, item := range items {
		if item.Failure != "" {
			failures = append(failures, ports.RevalidationFailure{
				TenantID:       item.Key.Tenant,
				ProductID:      item.Key.Product,
				ProductVersion: item.Version,
				SourceRevision: item.Revision,
				Diagnostic:     item.Failure,
			})
			continue
		}
		product := item.Product
		if product.State().Card.Publication.Version < version && product.RevalidateWith(checker, now) {
			products = append(products, product)
		}
	}
	return products, failures
}

func (s *Service) evaluateRevalidation(
	ctx context.Context,
	tx ports.Tx,
	products []*catalog.Product,
	definition taxonomy.Definition,
	checker taxonomy.Checker,
) ([]ports.RevalidationChange, error) {
	changes := make([]ports.RevalidationChange, 0, len(products))
	if len(products) == 0 {
		return changes, nil
	}
	if s.evaluator != nil {
		evaluator, ok := s.evaluator.(ports.RevalidationEvaluator)
		if !ok {
			return nil, errBatchEvaluator
		}
		return evaluator.PlanRevalidation(ctx, tx, products, definition, checker, s.clock.Now())
	}
	for _, product := range products {
		changes = append(changes, ports.RevalidationChange{Product: product})
	}
	return changes, nil
}

func prepareRevalidationTasks(
	changes []ports.RevalidationChange,
	now time.Time,
) ([]ports.RevalidationChange, []ports.RevalidationFailure) {
	ready := changes[:0]
	failures := []ports.RevalidationFailure{}
	for _, change := range changes {
		state := change.Product.State()
		if change.Failure != "" {
			failures = append(failures, ports.RevalidationFailure{
				TenantID:       state.TenantID,
				ProductID:      state.ProductID,
				ProductVersion: change.Product.OriginVersion(),
				SourceRevision: state.Revision,
				Diagnostic:     change.Failure,
			})
			continue
		}
		change.Tasks = append(
			change.Tasks,
			moderationstatus.OfferTask(state.TenantID, state.ProductID, now),
			appcatalog.SyncOfferTask(state.TenantID, state.ProductID, now),
		)
		ready = append(ready, change)
	}
	return ready, failures
}

func observeRevalidation(tx ports.Tx, changes []ports.RevalidationChange, failures []ports.RevalidationFailure) {
	failed := make(map[ids.OfferID]bool, len(failures))
	for _, failure := range failures {
		failed[ids.OfferID{Tenant: failure.TenantID, Product: failure.ProductID}] = true
	}
	for _, change := range changes {
		state := change.Product.State()
		if failed[ids.OfferID{Tenant: state.TenantID, Product: state.ProductID}] {
			continue
		}
		for _, observe := range change.Observations {
			tx.AfterCommit(observe)
		}
	}
}

// RevalidationFailures читает одну страницу диагностики без обхода каталога и COUNT всех товаров.
func (s *Service) RevalidationFailures(
	ctx context.Context,
	after ports.OfferKey,
	limit int,
) ([]ports.RevalidationFailure, error) {
	if limit < 1 || limit > 100 {
		return nil, taxonomy.ErrInvalid.WithMessage("limit должен быть от 1 до 100")
	}
	var result []ports.RevalidationFailure
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		result, err = tx.Revalidation().Failures(ctx, after, limit)
		return err
	})
	return result, err
}
