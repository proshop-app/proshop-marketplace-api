//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// Барьер гарантирует, что событие и догон оба дошли до вставки отсутствующего участника.
type candidateRaceUOW struct {
	ports.UnitOfWork
	ready   chan struct{}
	release chan struct{}
}

type candidateRaceTx struct {
	ports.Tx
	ready   chan struct{}
	release chan struct{}
}

type candidateRaceRepository struct {
	participant.Repository
	ready   chan struct{}
	release chan struct{}
}

func (u candidateRaceUOW) Do(ctx context.Context, fn func(context.Context, ports.Tx) error) error {
	return u.UnitOfWork.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return fn(ctx, candidateRaceTx{Tx: tx, ready: u.ready, release: u.release})
	})
}

func (tx candidateRaceTx) Participants() participant.Repository {
	return candidateRaceRepository{Repository: tx.Tx.Participants(), ready: tx.ready, release: tx.release}
}

func (r candidateRaceRepository) Add(ctx context.Context, p *participant.Participant) error {
	r.ready <- struct{}{}
	select {
	case <-r.release:
		return r.Repository.Add(ctx, p)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestCandidateRepositoryQueueAndConflict(t *testing.T) {
	uow := postgres.NewUnitOfWork(testPool(t))
	ctx := context.Background()
	seedTenant(t, uow, "t1", "shop", participant.TenantActive)
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		tenants, err := tx.Registry().List(ctx, "", 1)
		if err != nil {
			return err
		}
		if len(tenants) != 1 || tenants[0].ID() != "t1" {
			return fmt.Errorf("%+v", tenants)
		}
		p, err := participant.New(tenants[0], "Магазин", at(0))
		if err != nil {
			return err
		}
		if err := tx.Participants().Add(ctx, p); err != nil {
			return err
		}
		_, count, err := tx.Participants().List(ctx, participant.Page{Queue: true, Limit: 10})
		if err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("%+v", count)
		}
		if _, err := p.ApplyShopSettings(
			w3RepositoriesIntegrationSubsidy,
			money.Money{},
			w3RepositoriesIntegrationDelivery,
			at(1),
		); err != nil {
			return err
		}
		if _, err := p.ApplyStoreCard(
			participant.Profile{},
			participant.CardReadiness{Ready: true},
			nil,
			at(1),
		); err != nil {
			t.Fatal(err)
		}
		if err := tx.Participants().Update(ctx, p); err != nil {
			return err
		}
		list, count, err := tx.Participants().List(ctx, participant.Page{Queue: true, Limit: 10})
		if err != nil {
			return err
		}
		if count != 1 || !list[0].InQueue() {
			return fmt.Errorf("%+v", count)
		}
		stale := participant.Restore(p.State())
		if _, err := p.Return(at(2)); err != nil {
			return err
		}
		p.RecordDecision(participant.DecisionRecord{
			Action:   "return",
			Actor:    "operator",
			At:       at(2),
			Decision: participant.Decision{Note: "Исправьте"},
		})
		if err := tx.Participants().Update(ctx, p); err != nil {
			return err
		}
		read, err := tx.Participants().Get(ctx, "t1")
		if err != nil {
			return err
		}
		if read.Stage() != participant.StageReturned || read.State().LastDecision.Note != "Исправьте" {
			return fmt.Errorf("%+v", read.State())
		}
		if _, err := stale.ApplyStoreCard(
			participant.Profile{},
			participant.CardReadiness{Ready: true},
			nil,
			at(3),
		); err != nil {
			t.Fatal(err)
		}
		if _, err := stale.Enable(at(3)); err != nil {
			t.Fatal(err)
		}
		if err := tx.Participants().Update(ctx, stale); !errors.Is(err, participant.ErrConflict) {
			return fmt.Errorf("%w", err)
		}
		if _, err := read.Submit(at(3)); err != nil {
			return err
		}
		return tx.Participants().Update(ctx, read)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCandidateDuplicateKeepsTransactionUsable(t *testing.T) {
	uow := postgres.NewUnitOfWork(testPool(t))
	ctx := context.Background()
	seedTenant(t, uow, "t1", "shop", participant.TenantActive)
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		tenant, err := tx.Registry().Get(ctx, "t1")
		if err != nil {
			return err
		}
		p, err := participant.New(tenant, "Магазин", at(0))
		if err != nil {
			return err
		}
		if err := tx.Participants().Add(ctx, p); err != nil {
			return err
		}
		if err := tx.Participants().Add(ctx, p); !errors.Is(err, participant.ErrAlreadyExists) {
			return fmt.Errorf("повтор вставки: %w", err)
		}
		count, err := tx.Participants().Count(ctx, participant.Page{Stage: participant.StageCandidate})
		if err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("%+v", count)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCandidateMigrationStartsReviewWithoutBackfill(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if err := postgres.MigrateDownTo(ctx, pool, 36); err != nil {
		t.Fatal(err)
	}
	seed := `
  INSERT INTO tenants (tenant_id, slug, status, event_at, version, updated_at)
  VALUES ('t1', 'shop', 'active', now(), 1, now());
  INSERT INTO participants (tenant_id, slug, name, status, enabled, version, created_at, updated_at)
  VALUES ('t1', 'shop', 'Магазин', 'connected', true, 7, now(), now());`
	if _, err := pool.Exec(ctx, seed); err != nil {
		t.Fatal(err)
	}
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	uow := postgres.NewUnitOfWork(pool)
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		p, err := tx.Participants().Get(ctx, "t1")
		if err != nil {
			return err
		}
		state := p.State()
		if state.Stage != participant.StageCandidate || !state.AwaitingReview || state.Enabled ||
			state.LastDecision != nil || state.Version != 8 {
			return fmt.Errorf("%+v", state)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCandidateRegistryBackfillRace(t *testing.T) {
	base := postgres.NewUnitOfWork(testPool(t))
	seedTenant(t, base, "t1", "shop", participant.TenantActive)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	uow := candidateRaceUOW{UnitOfWork: base, ready: make(chan struct{}, 2), release: make(chan struct{})}
	clock := fake.NewClock(at(1))
	identities := &fake.IDs{}
	registry := appparticipant.NewRegistry(uow, clock, identities)
	platform := fake.NewPlatform()
	platform.SetSettings("t1", ports.ShopSettings{
		Delivery: w3RepositoriesIntegrationDelivery,
		Subsidy:  w3RepositoriesIntegrationSubsidy,
	})
	settings := appparticipant.NewSettings(uow, platform, clock, identities)
	done := make(chan error, 2)
	go func() {
		done <- registry.Apply(ctx, appparticipant.RegistryEvent{
			EventID: "race-event",
			Event:   registryEvent("t1", "shop", participant.TenantActive, 0),
		})
	}()
	go func() {
		result, err := settings.Sync(ctx)
		for _, failure := range result.Failed {
			err = errors.Join(err, failure)
		}
		done <- err
	}()
	for range 2 {
		select {
		case <-uow.ready:
		case <-ctx.Done():
			t.Fatal("оба сценария не дошли до вставки", ctx.Err())
		}
	}
	close(uow.release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	service := appparticipant.NewService(base, clock, identities)
	history, err := service.ListDecisions(ctx, "t1", participant.Page{Limit: 10})
	if err != nil || history.Total != 1 || history.Items[0].Action != "candidate" {
		t.Fatal(history, err)
	}
	// Повтор события проверяет, что проигравшая вставка не откатила inbox.
	if err := registry.Apply(ctx, appparticipant.RegistryEvent{
		EventID: "race-event",
		Event:   registryEvent("t1", "shop", participant.TenantActive, 0),
	}); err != nil {
		t.Fatal(err)
	}
}
