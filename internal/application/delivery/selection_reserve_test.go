package delivery_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	appdelivery "github.com/proshop-app/proshop-marketplace/internal/application/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/identity"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Резерв закреплён за покупателем до конца общей загрузки, а не до ухода его запроса.
func TestPointLimitsColdLoadsPerBuyerUntilCompletion(t *testing.T) {
	store, platform := fake.NewStore(), fake.NewPlatform()
	seedCatalogueSources(store, platform, 4)
	service := appdelivery.NewService(
		store,
		platform,
		fake.NewPickupCache(),
		appdelivery.Config{Attempts: 3, TTL: time.Hour, QuoteTimeout: time.Second},
	)
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	finish := func() {
		releaseOnce.Do(func() {
			close(release)
		})
	}
	defer finish()
	platform.BeforePickup = func(ctx context.Context, _ ids.TenantID) error {
		if calls.Add(1) != 1 {
			return nil
		}
		close(started)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan error, 1)
	go func() {
		_, err := service.Point(ctx, "buyer-a", "cdek", "P1", "Первый")
		first <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("загрузка не началась")
	}
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	joined, stop := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer stop()
	if _, err := service.Point(joined, "buyer-a", "cdek", "P1", "Первый"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("тот же ключ должен ждать единственную загрузку", err)
	}
	_, lookupErr := service.Point(context.Background(), "buyer-a", "cdek", "P1", "Другой")
	if !errors.Is(lookupErr, delivery.ErrSelectionBusy) {
		t.Fatal("покупатель занял второй холодный каталог", lookupErr)
	}
	if _, err := service.Point(context.Background(), "buyer-b", "cdek", "P1", "Другой"); err != nil {
		t.Fatal("личный лимит закрыл чужой выбор", err)
	}
	if _, err := service.Point(context.Background(), "buyer-a", "cdek", "P1", "Другой"); err != nil {
		t.Fatal("тёплый каталог не должен занимать резерв", err)
	}
	if calls.Load() != 2 {
		t.Fatal("один покупатель начал несколько загрузок", calls.Load())
	}
	_, lookupErr = service.Point(context.Background(), "", "cdek", "P1", "Без покупателя")
	if !errors.Is(lookupErr, identity.ErrUnauthorized) {
		t.Fatal("вход в резерв без покупателя", lookupErr)
	}
	finish()
	if _, err := service.Point(context.Background(), "buyer-a", "cdek", "P1", "Первый"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Point(context.Background(), "buyer-a", "cdek", "P1", "Следующий"); err != nil {
		t.Fatal("после загрузки личный резерв не освободился", err)
	}
	if calls.Load() != 3 {
		t.Fatal("повтор того же ключа создал лишнюю загрузку", calls.Load())
	}
}
