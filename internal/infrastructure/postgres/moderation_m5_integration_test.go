//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/random"
)

const (
	updateVerdictLog                = `UPDATE moderation_verdict_log SET status = 'allowed'`
	deleteVerdictLog                = `DELETE FROM moderation_verdict_log`
	insertLegacyParticipantDecision = `INSERT INTO participant_decisions
 (id, tenant_id, operator_id, operator_name, action, value, reason, note, at)
 VALUES ($1, 't1', '', '', 'enable', '', '', '', now())`
)

func TestM5VerdictStorage(t *testing.T) {
	pool := pgtest.Pool(t)
	uow := postgres.NewUnitOfWork(pool)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		v := moderation.NewVerdict("t1", "p1")
		v.Evaluate(1, 2, moderation.Rules{Revision: 3}, moderation.Input{Probation: true}, now)
		if err := tx.ModerationVerdicts().Save(ctx, v); err != nil {
			return err
		}
		v.Decide(moderation.Reject, "Текст", now)
		v.RecordDecision(moderation.Actor{ID: "o1", Name: "Оператор"}, moderation.VerdictDecision{
			Action: moderation.Reject,
			Reason: moderation.DecisionReason("r1", "Текст"),
			Note:   "Заметка",
			At:     now,
		})
		return tx.ModerationVerdicts().Save(ctx, v)
	})
	if err != nil {
		t.Fatal(err)
	}
	rollback := errors.New("откат")
	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		v, err := tx.ModerationVerdicts().Get(ctx, "t1", "p1")
		if err != nil {
			return err
		}
		state := v.State()
		if state.Operator.ID != "o1" || state.Decision.Reason.Text != "Текст" {
			return fmt.Errorf("%+v", state)
		}
		filter := moderation.Filter{DecidedBy: moderation.Operator, Limit: 10}
		page, total, err := tx.ModerationVerdicts().List(ctx, filter)
		if err != nil {
			return err
		}
		if total != 1 || len(page) != 1 {
			return fmt.Errorf("%+v %+v", page, total)
		}
		v.Evaluate(2, 2, moderation.Rules{Revision: 3}, moderation.Input{}, now)
		if err := tx.ModerationVerdicts().Save(ctx, v); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		history, err := tx.ModerationVerdicts().History(ctx, "t1", "p1")
		if err != nil {
			return err
		}
		if len(history) != 2 || history[0].Status != moderation.Rejected || history[0].Operator.ID != "o1" ||
			history[0].RulesRevision != 3 || history[0].TaxonomyVersion != 2 || !history[0].UpdatedAt.Equal(now) {
			return fmt.Errorf("%+v", history)
		}
		v, err := tx.ModerationVerdicts().Get(ctx, "t1", "p1")
		if err != nil {
			return err
		}
		for revision := int64(2); revision <= 55; revision++ {
			v.Evaluate(revision, 2, moderation.Rules{Revision: 3}, moderation.Input{}, now)
			if err := tx.ModerationVerdicts().Save(ctx, v); err != nil {
				return err
			}
		}
		history, err = tx.ModerationVerdicts().History(ctx, "t1", "p1")
		if err == nil && (len(history) != 50 || history[0].Revision != 55) {
			return fmt.Errorf("%+v", history)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{updateVerdictLog, deleteVerdictLog} {
		if _, err := pool.Exec(ctx, query); err == nil {
			t.Fatal("журнал допускает изменение")
		}
	}
}

func TestM5ParticipantPreviousValue(t *testing.T) {
	pool := pgtest.Pool(t)
	uow := postgres.NewUnitOfWork(pool)
	ctx := context.Background()
	now := time.Now().UTC()
	identities := random.IDs{}
	seedTenant(t, uow, "t1", "t1", participant.TenantActive)
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		tenant, err := tx.Registry().Get(ctx, "t1")
		if err != nil {
			return err
		}
		member, err := participant.New(tenant, "Магазин", now)
		if err != nil {
			return err
		}
		return tx.Participants().Add(ctx, member)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, insertLegacyParticipantDecision, identities.NewUUID()); err != nil {
		t.Fatal(err)
	}
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		if err := tx.ParticipantDecisions().Add(ctx, participant.DecisionRecord{
			ID:            identities.NewUUID(),
			TenantID:      "t1",
			Actor:         "operator",
			Action:        "trust",
			Value:         participant.Trusted,
			PreviousValue: participant.Probation,
			Decision:      participant.Decision{Note: "Заметка"},
			At:            now,
		}); err != nil {
			return err
		}
		records, total, err := tx.ParticipantDecisions().List(ctx, "t1", participant.Page{Limit: 10})
		if err != nil {
			return err
		}
		if total != 2 {
			return fmt.Errorf("%+v", records)
		}
		for _, record := range records {
			expected := ""
			if record.Action == "trust" {
				expected = participant.Probation
			}
			if record.PreviousValue != expected {
				return fmt.Errorf("%+v", record)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestM5QueueStorage(t *testing.T) {
	pool := pgtest.Pool(t)
	uow := postgres.NewUnitOfWork(pool)
	ctx := context.Background()
	identities := random.IDs{}
	now := time.Now().UTC()
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		for i := 0; i < 2; i++ {
			v := moderation.VerdictState{
				TenantID:  "t1",
				ProductID: "p1",
				Revision:  int64(i + 1),
				Status:    moderation.Pending,
				Reasons:   []moderation.Reason{{Key: "moderation.probation", Message: "Испытание"}},
			}
			if i == 1 {
				v.ProductID = "p2"
			}
			openedAt := now.Add(time.Duration(i) * time.Minute)
			c := moderation.NewCase(identities.NewUUID(), v, moderation.Rule, "", openedAt, nil)
			if err := tx.ModerationCases().Save(ctx, c); err != nil {
				return err
			}
		}
		for _, order := range []string{"oldest", "newest"} {
			page, total, err := tx.ModerationCases().List(ctx, moderation.Filter{
				Order:  order,
				Reason: "moderation.probation",
				Limit:  1,
			})
			if err != nil {
				return err
			}
			expected := int64(1)
			if order == "newest" {
				expected = 2
			}
			if total != 2 || len(page) != 1 || page[0].Revision != expected {
				return fmt.Errorf("%+v %+v", page, total)
			}
		}
		filter := moderation.Filter{Reason: "moderation.image_missing", Limit: 10}
		_, total, err := tx.ModerationCases().List(ctx, filter)
		if err == nil && total != 0 {
			return fmt.Errorf("%+v", total)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
