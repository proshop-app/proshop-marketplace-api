package moderation_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
)

type delayedBulkUOW struct {
	ports.UnitOfWork
	clock *fake.Clock
}

func (u delayedBulkUOW) Do(ctx context.Context, fn func(context.Context, ports.Tx) error) error {
	u.clock.Advance(31 * 24 * time.Hour)
	return u.UnitOfWork.Do(ctx, fn)
}

func TestBulkKeyExpiryIsCheckedAfterWaitingForTransaction(t *testing.T) {
	e := setup()
	e.project(t, "p1", 1)
	uow := delayedBulkUOW{UnitOfWork: e.store, clock: e.clock}
	service := appmoderation.NewService(uow, e.clock, &fake.IDs{}, appmoderation.Config{})
	_, err := service.BulkAllow(context.Background(), appmoderation.BulkCommand{
		Selector:       &moderation.BulkSelector{TenantID: "t1", Reasons: []string{"moderation.probation"}},
		Note:           "Проверено",
		Operator:       moderation.Actor{ID: "o1"},
		IdempotencyKey: fmt.Sprintf("%d:waiting", e.clock.Now().UnixMilli()),
	})
	if !errors.Is(err, moderation.ErrKeyExpired) || e.view(t, "p1").Case.Status != moderation.Open {
		t.Fatal("ключ истёк в ожидании, но отбор был исполнен", err)
	}
}
