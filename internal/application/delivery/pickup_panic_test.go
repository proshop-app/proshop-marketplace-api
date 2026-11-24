package delivery_test

import (
	"context"
	"errors"
	"testing"
	"time"

	app "github.com/proshop-app/proshop-marketplace/internal/application/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestPanickingPickupLoadClosesFlightForEveryWaiter(t *testing.T) {
	service, platform, _ := setup(t, 1)
	platform.BeforePickup = func(context.Context, ids.TenantID) error {
		panic("сбой разбора списка пунктов")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	_, err := service.Search(ctx, "cdek", "Город")
	if !errors.Is(err, errs.ErrPanic) || time.Since(started) >= time.Second {
		t.Fatalf("паника ведущего закрывает рейс сразу: %v за %s", err, time.Since(started))
	}
	// Город не застревает в рейсе: следующий запрос получает короткий отказ, а не ждёт срока.
	started = time.Now()
	if _, err := service.Search(ctx, "cdek", "Город"); !errors.Is(err, app.ErrSellerUnreachable) ||
		time.Since(started) >= time.Second {
		t.Fatalf("после паники город отвечает отказом из кэша: %v за %s", err, time.Since(started))
	}
}
