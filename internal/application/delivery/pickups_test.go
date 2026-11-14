package delivery_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	app "github.com/proshop-app/proshop-marketplace/internal/application/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// waitingContext сигнализирует, когда вызов уже вошёл в ожидание общей загрузки.
type waitingContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *waitingContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func setup(t *testing.T, attempts int) (*app.Service, *fake.Platform, *fake.Store) {
	t.Helper()
	store := fake.NewStore()
	platform := fake.NewPlatform()
	// Обратный порядок создания не должен менять порядок опроса.
	for _, tenant := range []ids.TenantID{"t2", "t1"} {
		store.SeedTenant(participant.TenantState{TenantID: tenant, Status: participant.TenantActive})
		store.SeedParticipant(participant.State{
			TenantID: tenant,
			Stage:    participant.StageConnected,
			Enabled:  true,
			Subsidy:  participant.Subsidy{Enabled: true, Type: participant.SubsidyFixed},
			Delivery: participant.DeliverySettings{Regions: []string{"RU-AD"}, Carriers: []string{"cdek"}},
		})
	}
	service := app.NewService(store, platform, fake.NewPickupCache(), app.Config{
		Attempts:     attempts,
		TTL:          time.Hour,
		QuoteTimeout: 20 * time.Millisecond,
	})
	return service, platform, store
}

func answer() ports.PickupPoints {
	return ports.PickupPoints{
		Available: true,
		Items: []delivery.PickupPoint{
			{Carrier: "cdek", Code: "p1", City: "Город", RegionCode: "RU-AD", Address: "Адрес"},
		},
	}
}

func TestFallbackAndCache(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(map[bool]string{false: "служба не подключена", true: "нет ответа"}[unavailable], func(t *testing.T) {
			service, platform, _ := setup(t, 3)
			if unavailable {
				platform.Fail("t1", errors.New("нет связи"))
			}
			platform.SetPickupPoints("t2", answer())
			for _, city := range []string{"Город", "  ГОРОД  "} {
				points, err := service.Search(context.Background(), "cdek", city)
				if err != nil || len(points) != 1 {
					t.Fatal(points, err)
				}
			}
			calls := platform.PickupCalls()
			if len(calls) != 2 || calls[0].Tenant != "t1" || calls[1].Tenant != "t2" {
				t.Fatal(calls)
			}
			if _, err := service.Search(context.Background(), "cdek", "Другой город"); err != nil {
				t.Fatal(err)
			}
			if len(platform.PickupCalls()) != 4 {
				t.Fatal(platform.PickupCalls())
			}
		})
	}
}

func TestUnavailableLimitAndEmpty(t *testing.T) {
	service, platform, _ := setup(t, 1)
	platform.Fail("t1", errors.New("нет связи"))
	platform.SetPickupPoints("t2", answer())
	if _, err := service.Search(context.Background(), "cdek", "Город"); !errors.Is(err, app.ErrSellerUnreachable) {
		t.Fatal(err)
	}
	if len(platform.PickupCalls()) != 1 {
		t.Fatal(platform.PickupCalls())
	}
	service, platform, _ = setup(t, 3)
	platform.Fail("t1", errors.New("нет связи"))
	platform.Fail("t2", errors.New("нет связи"))
	for i := 0; i < 2; i++ {
		if _, err := service.Search(context.Background(), "cdek", "Город"); !errors.Is(err, app.ErrSellerUnreachable) {
			t.Fatal(err)
		}
	}
	if len(platform.PickupCalls()) != 4 {
		t.Fatal("ошибка закэширована")
	}
	points, err := service.Search(context.Background(), "yandex", "Город")
	if err != nil || len(points) != 0 || len(platform.PickupCalls()) != 4 {
		t.Fatal(points, err)
	}
}

func TestEmptySuccessAndRefusalsAreNotCached(t *testing.T) {
	for _, available := range []bool{false, true} {
		service, platform, _ := setup(t, 3)
		if available {
			platform.SetPickupPoints("t1", ports.PickupPoints{Available: true})
		}
		for i := 0; i < 2; i++ {
			points, err := service.Search(context.Background(), "cdek", "Город")
			if err != nil || len(points) != 0 {
				t.Fatal(points, err)
			}
		}
		expected := 4
		if available {
			expected = 2
		}
		if len(platform.PickupCalls()) != expected {
			t.Fatal(platform.PickupCalls())
		}
	}
}

func TestConcurrentMissAndDeadline(t *testing.T) {
	service, platform, _ := setup(t, 3)
	platform.SetPickupPoints("t2", answer())
	platform.BeforePickup = func(ctx context.Context, tenant ids.TenantID) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("нет дедлайна магазина")
		}
		if tenant == "t1" {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			points, err := service.Search(context.Background(), "cdek", "Город")
			if err != nil || len(points) != 1 {
				t.Error(points, err)
			}
		}()
	}
	wg.Wait()
	if len(platform.PickupCalls()) != 2 {
		t.Fatal(platform.PickupCalls())
	}
}

func TestLeaderCancellationDoesNotCancelSharedLoad(t *testing.T) {
	_, platform, store := setup(t, 3)
	service := app.NewService(store, platform, fake.NewPickupCache(), app.Config{
		Attempts:     3,
		TTL:          time.Hour,
		QuoteTimeout: time.Second,
	})
	platform.SetPickupPoints("t1", answer())
	started := make(chan struct{})
	release := make(chan struct{})
	platform.BeforePickup = func(ctx context.Context, _ ids.TenantID) error {
		close(started)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	leader, cancelLeader := context.WithCancel(context.Background())
	defer cancelLeader()
	leaderResult := make(chan error, 1)
	go func() {
		_, err := service.Search(leader, "cdek", "Город")
		leaderResult <- err
	}()
	<-started
	timeout, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	waiter := &waitingContext{Context: timeout, waiting: make(chan struct{})}
	waiterResult := make(chan error, 1)
	go func() {
		points, err := service.Search(waiter, "cdek", "Город")
		if err == nil && (len(points) != 1 || points[0].Code != "p1") {
			err = errors.New("ожидающий не получил пункт")
		}
		waiterResult <- err
	}()
	<-waiter.waiting
	cancelLeader()
	select {
	case err := <-leaderResult:
		if !errors.Is(err, context.Canceled) {
			t.Error("ведущий не получил свою отмену", err)
		}
	case <-timeout.Done():
		t.Error("ведущий продолжает ждать магазин после отмены")
	}
	close(release)
	if err := <-waiterResult; err != nil {
		t.Fatal("отмена ведущего затронула ожидающего", err)
	}
	if len(platform.PickupCalls()) != 1 {
		t.Fatal("ожидающий создал отдельную загрузку", platform.PickupCalls())
	}
}
