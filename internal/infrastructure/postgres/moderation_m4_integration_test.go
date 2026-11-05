//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/random"
)

const invalidDecisionActor = `INSERT INTO participant_decisions
 (id, tenant_id, operator_id, operator_name, action, value, reason, note, at, actor)
 VALUES ($1, 't1', '', '', 'enable', '', '', '', now(), 'unknown')`

func TestModerationM4Storage(t *testing.T) {
	pool := pgtest.Pool(t)
	uow := postgres.NewUnitOfWork(pool)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	seedTenant(t, uow, "t1", "t1", participant.TenantActive)
	identities := random.IDs{}
	v := moderation.VerdictState{TenantID: "t1", ProductID: "p1", Revision: 1, Status: moderation.Pending}
	card := moderation.ReviewCard{Name: "Товар", Price: 100, Seller: moderation.ReviewSeller{Name: "Магазин"}}
	c := moderation.NewCase(identities.NewUUID(), v, moderation.Rule, "", now, &card)
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		tenant, err := tx.Registry().Get(ctx, "t1")
		if err != nil {
			return err
		}
		member, err := participant.New(tenant, "Магазин", now)
		if err != nil {
			return err
		}
		if err := tx.Participants().Add(ctx, member); err != nil {
			return err
		}
		if err := tx.ModerationCases().Save(ctx, c); err != nil {
			return err
		}
		if err := tx.ModerationDecisions().Add(ctx, moderation.Decision{
			ID:            identities.NewUUID(),
			CaseID:        c.State().ID,
			Action:        moderation.Allow,
			Reason:        "r1",
			ReasonMessage: "Текст",
			Note:          "Заметка",
			Revision:      1,
			At:            now,
		}); err != nil {
			return err
		}
		return tx.ParticipantDecisions().Add(ctx, participant.DecisionRecord{
			ID:       identities.NewUUID(),
			TenantID: "t1",
			Actor:    "system",
			Action:   "trust",
			Value:    participant.Trusted,
			Decision: participant.Decision{Note: "Одобрено 1 предложений"},
			At:       now,
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		read, err := tx.ModerationCases().Get(ctx, c.State().ID)
		if err != nil {
			return err
		}
		if read.State().Card == nil || read.State().Card.Name != "Товар" || read.State().Card.Price != 100 {
			t.Fatal(read.State())
		}
		v.Revision = 2
		card.Name = "Изменено"
		read.Refresh(v, now, &card)
		if err := tx.ModerationCases().Save(ctx, read); err != nil {
			return err
		}
		read, err = tx.ModerationCases().Get(ctx, c.State().ID)
		if err != nil {
			return err
		}
		if read.State().Card.Name != "Изменено" {
			t.Fatal(read.State())
		}
		decisions, err := tx.ModerationDecisions().List(ctx, "t1", "p1")
		if err != nil {
			return err
		}
		if len(decisions) != 1 || decisions[0].ReasonMessage != "Текст" {
			t.Fatal(decisions)
		}
		history, _, err := tx.ParticipantDecisions().List(ctx, "t1", participant.Page{Limit: 10})
		if err != nil {
			return err
		}
		if len(history) != 1 || history[0].Actor != "system" || history[0].Reason != "" {
			t.Fatal(history)
		}
		if err := tx.ParticipantDecisions().Add(ctx, participant.DecisionRecord{
			ID:       identities.NewUUID(),
			TenantID: "t1",
			Action:   "disable",
			Decision: participant.Decision{Reason: "r1", ReasonMessage: "Текст", Note: "Заметка"},
			At:       now.Add(time.Second),
		}); err != nil {
			return err
		}
		history, _, err = tx.ParticipantDecisions().List(ctx, "t1", participant.Page{Limit: 1})
		if err != nil {
			return err
		}
		if history[0].Actor != "operator" || history[0].ReasonMessage != "Текст" {
			t.Fatal(history)
		}
		for _, trust := range []string{participant.Probation, participant.Trusted} {
			items, total, err := tx.Participants().List(ctx, participant.Page{Trust: trust, Limit: 10})
			if err != nil {
				return err
			}
			expected := 0
			if trust == participant.Probation {
				expected = 1
			}
			if total != expected || len(items) != expected {
				t.Fatal(items, total)
			}
		}
		reason := moderation.ReasonCode{Code: "r1", Text: "Текст"}
		if err := tx.ModerationRules().AddReason(ctx, reason); err != nil {
			return err
		}
		if err := tx.ModerationRules().AddReason(ctx, reason); !errors.Is(err, moderation.ErrReasonExists) {
			t.Fatal(err)
		}
		reason.Text = "Изменено"
		if err := tx.ModerationRules().PutReason(ctx, reason); err != nil {
			return err
		}
		reason.Code = "r2"
		if err := tx.ModerationRules().PutReason(ctx, reason); !errors.Is(err, moderation.ErrNotFound) {
			t.Fatal(err)
		}
		v.ProductID = "p2"
		legacy := moderation.NewCase(identities.NewUUID(), v, moderation.Rule, "", now, nil)
		if err := tx.ModerationCases().Save(ctx, legacy); err != nil {
			return err
		}
		read, err = tx.ModerationCases().Get(ctx, legacy.State().ID)
		if err != nil {
			return err
		}
		if read.State().Card != nil {
			t.Fatal("NULL превратился в пустую карточку")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, invalidDecisionActor, identities.NewUUID()); err == nil {
		t.Fatal("ограничение actor не сработало")
	}
}
