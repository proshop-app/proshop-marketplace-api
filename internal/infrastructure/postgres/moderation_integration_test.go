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
	updateModerationJournal  = `UPDATE moderation_decisions SET note = 'изменение'`
	deleteModerationJournal  = `DELETE FROM moderation_decisions`
	updateParticipantJournal = `UPDATE participant_decisions SET note = 'изменение'`
	deleteParticipantJournal = `DELETE FROM participant_decisions`
)

func TestModerationRepositories(t *testing.T) {
	pool := pgtest.Pool(t)
	uow := postgres.NewUnitOfWork(pool)
	ctx := context.Background()
	now := time.Now().UTC()
	identities := random.IDs{}
	v := moderation.NewVerdict("t1", "p1")
	v.Evaluate(1, 1, moderation.Rules{Revision: 1}, moderation.Input{Probation: true}, now)
	c := moderation.NewCase(identities.NewUUID(), v.State(), moderation.Rule, "", now, nil)
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		if err := tx.ModerationVerdicts().Save(ctx, v); err != nil {
			return err
		}
		return tx.ModerationCases().Save(ctx, c)
	})
	if err != nil {
		t.Fatal(err)
	}
	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		read, err := tx.ModerationVerdicts().Get(ctx, "t1", "p1")
		if err != nil {
			return err
		}
		if read.State().Status != moderation.Pending || read.Origin().Version != 1 {
			return fmt.Errorf("%+v", read.State())
		}
		opened, err := tx.ModerationCases().Open(ctx, "t1", "p1")
		if err != nil {
			return err
		}
		if opened.State().ID != c.State().ID {
			return fmt.Errorf("%+v", opened.State())
		}
		read.Evaluate(2, 1, moderation.Rules{Revision: 1}, moderation.Input{Probation: true}, now)
		if err := tx.ModerationVerdicts().Save(ctx, read); err != nil {
			return err
		}
		stale := moderation.RestoreVerdict(v.State())
		stale.Evaluate(3, 1, moderation.Rules{Revision: 1}, moderation.Input{}, now)
		return tx.ModerationVerdicts().Save(ctx, stale)
	})
	if !errors.Is(err, moderation.ErrConflict) {
		t.Fatalf("конфликт вердикта: %v", err)
	}
	duplicate := moderation.NewCase(identities.NewUUID(), v.State(), moderation.Rule, "", now, nil)
	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error { return tx.ModerationCases().Save(ctx, duplicate) })
	if !errors.Is(err, moderation.ErrCaseConflict) {
		t.Fatalf("уникальность открытого дела: %v", err)
	}
	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		read, err := tx.ModerationCases().Get(ctx, c.State().ID)
		if err != nil {
			return err
		}
		stale := moderation.RestoreCase(read.State())
		if _, err := read.Decide(1, 1, "allow", "Проверено", now); err != nil {
			return err
		}
		if err := tx.ModerationCases().Save(ctx, read); err != nil {
			return err
		}
		if _, err := stale.Decide(1, 1, "reject", "Проверено", now); err != nil {
			return err
		}
		return tx.ModerationCases().Save(ctx, stale)
	})
	if !errors.Is(err, moderation.ErrCaseConflict) {
		t.Fatalf("конфликт дела: %v", err)
	}
	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		d := moderation.Decision{
			ID:       identities.NewUUID(),
			CaseID:   c.State().ID,
			Operator: moderation.Actor{ID: "o1", Name: "Оператор"},
			Action:   "allow",
			Note:     "Проверено",
			Revision: 1,
			At:       now,
		}
		if err := tx.ModerationDecisions().Add(ctx, d); err != nil {
			return err
		}
		decisions, err := tx.ModerationDecisions().List(ctx, "t1", "p1")
		if err != nil {
			return err
		}
		if len(decisions) != 1 || decisions[0].Operator.ID != "o1" {
			return fmt.Errorf("%+v", decisions)
		}
		cases, total, err := tx.ModerationCases().List(ctx, moderation.Filter{Status: "open", Limit: 10})
		if err != nil {
			return err
		}
		if total != 1 || len(cases) != 1 {
			return fmt.Errorf("%+v %+v", cases, total)
		}
		verdicts, total, err := tx.ModerationVerdicts().List(ctx, moderation.Filter{TenantID: "t1", Limit: 10})
		if total != 1 || len(verdicts) != 1 {
			return fmt.Errorf("%+v %+v", verdicts, total)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{updateModerationJournal, deleteModerationJournal} {
		if _, err := pool.Exec(ctx, query); err == nil {
			t.Fatal("журнал разрешил изменение")
		}
	}
}

func TestModerationRulesRepository(t *testing.T) {
	uow := postgres.NewUnitOfWork(pgtest.Pool(t))
	ctx := context.Background()
	phrase, err := moderation.NewStopPhrase("Товар", moderation.Actor{ID: "o1"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		repo := tx.ModerationRules()
		before, err := repo.Current(ctx)
		if err != nil {
			return err
		}
		if len(before.Phrases) != 0 {
			return fmt.Errorf("%+v", "словарь должен быть пустым")
		}
		if err := repo.AddPhrase(ctx, phrase); err != nil {
			return err
		}
		after, err := repo.Current(ctx)
		if err != nil {
			return err
		}
		if after.Revision != before.Revision+1 || len(after.Phrases) != 1 {
			return fmt.Errorf("%+v", after)
		}
		if err := repo.AddPhrase(ctx, phrase); !errors.Is(err, moderation.ErrPhraseExists) {
			return fmt.Errorf("%w", err)
		}
		if _, err := repo.RemovePhrase(ctx, phrase.Key); err != nil {
			return err
		}
		if changed, err := repo.RemovePhrase(ctx, phrase.Key); err != nil || changed {
			return fmt.Errorf("%+v %w", changed, err)
		}
		final, err := repo.Current(ctx)
		if err != nil {
			return err
		}
		if final.Revision != before.Revision+2 {
			return fmt.Errorf("%+v", final)
		}
		if err := repo.AddReason(ctx, moderation.ReasonCode{Code: "r1", Text: "Причина"}); err != nil {
			return err
		}
		reasons, err := repo.ListReasons(ctx)
		if len(reasons) != 1 || reasons[0].Code != "r1" {
			return fmt.Errorf("%+v", reasons)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestParticipantModerationJournal(t *testing.T) {
	pool := pgtest.Pool(t)
	uow := postgres.NewUnitOfWork(pool)
	ctx := context.Background()
	seedTenant(t, uow, "t1", "t1", participant.TenantActive)
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		tenant, err := tx.Registry().Get(ctx, "t1")
		if err != nil {
			return err
		}
		p, err := participant.New(tenant, "Магазин", time.Now())
		if err != nil {
			return err
		}
		if err := tx.Participants().Add(ctx, p); err != nil {
			return err
		}
		p.ApproveOffer(1, time.Now())
		if err := tx.Participants().Update(ctx, p); err != nil {
			return err
		}
		read, err := tx.Participants().Get(ctx, "t1")
		if err != nil {
			return err
		}
		if read.State().Trust != participant.Trusted || read.State().ApprovedOffers != 1 {
			return fmt.Errorf("%+v", read.State())
		}
		d := participant.DecisionRecord{
			ID:       random.IDs{}.NewUUID(),
			TenantID: "t1",
			Action:   "trust",
			Value:    participant.Trusted,
			At:       time.Now(),
		}
		if err := tx.ParticipantDecisions().Add(ctx, d); err != nil {
			return err
		}
		items, total, err := tx.ParticipantDecisions().List(ctx, "t1", participant.Page{Limit: 10})
		if total != 1 || len(items) != 1 || items[0].Value != participant.Trusted {
			return fmt.Errorf("%+v %+v", items, total)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{updateParticipantJournal, deleteParticipantJournal} {
		if _, err := pool.Exec(ctx, query); err == nil {
			t.Fatal("журнал разрешил изменение")
		}
	}
}

func TestModerationCachedDecisionAndStats(t *testing.T) {
	pool := pgtest.Pool(t)
	uow := postgres.NewUnitOfWork(pool)
	ctx := context.Background()
	now := time.Now().UTC()
	identities := random.IDs{}
	v := moderation.NewVerdict("t1", "p1")
	v.Evaluate(1, 1, moderation.Rules{Revision: 1}, moderation.Input{Probation: true}, now)
	c := moderation.NewCase(identities.NewUUID(), v.State(), moderation.Rule, "", now, nil)
	cached := moderation.CachedDecision{Fingerprint: "f1", Result: c.State(), ExpiresAt: now.Add(time.Hour)}
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		if err := tx.ModerationVerdicts().Save(ctx, v); err != nil {
			return err
		}
		if err := tx.ModerationCases().Save(ctx, c); err != nil {
			return err
		}
		_, found, err := tx.ModerationDecisions().Cached(ctx, "k1", now)
		if err != nil {
			return err
		}
		if found {
			return fmt.Errorf("%+v", "неожиданный кеш")
		}
		return tx.ModerationDecisions().Cache(ctx, "k1", cached)
	})
	if err != nil {
		t.Fatal(err)
	}
	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		value, found, err := tx.ModerationDecisions().Cached(ctx, "k1", now)
		if err != nil {
			return err
		}
		if !found || value.Result.ID != c.State().ID || value.Fingerprint != "f1" {
			return fmt.Errorf("%+v %+v", value, found)
		}
		_, found, err = tx.ModerationDecisions().Cached(ctx, "k1", cached.ExpiresAt)
		if err != nil {
			return err
		}
		if found {
			return fmt.Errorf("%+v", "просроченный ответ возвращён")
		}
		stats, err := tx.ModerationCases().OpenStats(ctx)
		if err != nil {
			return err
		}
		if len(stats) != 1 || stats[0].TenantID != "t1" || stats[0].Count != 1 {
			return fmt.Errorf("%+v", stats)
		}
		if err := tx.ModerationRules().AddReason(ctx, moderation.ReasonCode{Code: "r1", Text: "Причина"}); err != nil {
			return err
		}
		return tx.ModerationRules().PutReason(ctx, moderation.ReasonCode{Code: "r1", Text: "Текст"})
	})
	if err != nil {
		t.Fatal(err)
	}
}
