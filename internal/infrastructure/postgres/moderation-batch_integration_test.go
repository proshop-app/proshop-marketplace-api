//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestModerationBatchFeedback(t *testing.T) {
	uow := postgres.NewUnitOfWork(pgtest.Pool(t))
	err := uow.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		for i, offer := range []ids.OfferID{
			{Tenant: "t1", Product: "p1"},
			{Tenant: "t1", Product: "p2"},
			{Tenant: "t2", Product: "p1"},
		} {
			if err := seedBatchDecisions(ctx, tx, offer, i); err != nil {
				return err
			}
		}
		for _, tenant := range []ids.TenantID{"t1", "t2", "missing"} {
			if err := checkBatchDecisions(ctx, tx, tenant); err != nil {
				return err
			}
		}
		cases := []ids.UUID{}
		for i := range 4 {
			cases = append(cases, ids.UUID(fmt.Sprintf("00000000-0000-4000-8000-%012d", i)))
		}
		reasons, err := tx.ModerationCases().Reasons(ctx, cases)
		if err != nil || len(reasons) != 3 {
			return fmt.Errorf("причины дел: %+v: %w", reasons, err)
		}
		for _, id := range cases[:3] {
			c, err := tx.ModerationCases().Get(ctx, id)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(reasons[id], c.State().Reasons) {
				return fmt.Errorf("причины пачки отличаются от дела %s", id)
			}
		}
		empty, err := tx.ModerationDecisions().Latest(ctx, "t1", nil)
		if err != nil || len(empty) != 0 {
			return fmt.Errorf("пустая пачка решений: %+v: %w", empty, err)
		}
		emptyReasons, err := tx.ModerationCases().Reasons(ctx, nil)
		if err != nil || len(emptyReasons) != 0 {
			return fmt.Errorf("пустая пачка дел: %+v: %w", emptyReasons, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func seedBatchDecisions(ctx context.Context, tx ports.Tx, offer ids.OfferID, index int) error {
	now := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	id := ids.UUID(fmt.Sprintf("00000000-0000-4000-8000-%012d", index))
	state := moderation.VerdictState{
		TenantID:  offer.Tenant,
		ProductID: offer.Product,
		Revision:  1,
		Reasons:   []moderation.Reason{{Key: "review", Message: "Проверка"}},
	}
	c := moderation.NewCase(id, state, moderation.Rule, "", now, nil)
	if err := tx.ModerationCases().Save(ctx, c); err != nil {
		return err
	}
	for j := range 3 {
		if err := tx.ModerationDecisions().Add(ctx, moderation.Decision{
			ID:            ids.UUID(fmt.Sprintf("00000000-0000-4000-8000-%012d", 100+index*10+j)),
			CaseID:        id,
			Action:        moderation.Reject,
			Revision:      1,
			Reason:        "reason",
			ReasonMessage: "Причина",
			Note:          fmt.Sprintf("Решение %d", j),
			At:            now.Add(time.Duration(min(j, 1)) * time.Second),
		}); err != nil {
			return err
		}
	}
	return nil
}

func checkBatchDecisions(ctx context.Context, tx ports.Tx, tenant ids.TenantID) error {
	latest, err := tx.ModerationDecisions().Latest(ctx, tenant, []ids.ProductID{"p1", "p2", "missing"})
	if err != nil {
		return err
	}
	want := 0
	for _, product := range []ids.ProductID{"p1", "p2", "missing"} {
		history, err := tx.ModerationDecisions().List(ctx, tenant, product)
		if err != nil {
			return err
		}
		if len(history) > 0 {
			want++
			if !reflect.DeepEqual(latest[product], history[0]) || latest[product].Note != "Решение 2" {
				return fmt.Errorf("последнее решение пачки отличается от журнала %s/%s", tenant, product)
			}
		}
	}
	if len(latest) != want {
		return fmt.Errorf("чужие или отсутствующие товары попали в пачку: %+v", latest)
	}
	return nil
}
