package platformapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	appdelivery "github.com/proshop-app/proshop-marketplace/internal/application/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/platformguard"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
)

func TestColdPickupCatalogueCarriesDeadlineAcrossBothProviderStagesAndWarmsCache(t *testing.T) {
	fixture, err := os.ReadFile("testdata/pickups.json")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		deadline, err := strconv.ParseInt(r.Header.Get("x-marketplace-deadline-ms"), 10, 64)
		remaining := time.Until(time.UnixMilli(deadline))
		if err != nil || remaining < 9*time.Second || remaining > 10*time.Second {
			t.Errorf(
				"сервер не получил оставшийся длинный срок: %s, %v",
				r.Header.Get("x-marketplace-deadline-ms"),
				err,
			)
		}
		// Холодные определение города и список: каждый медленнее старого READ 250 мс,
		// вместе — старых потолков приложения, предохранителя и HTTP-клиента 5 с.
		for range 2 {
			select {
			case <-time.After(3 * time.Second):
			case <-r.Context().Done():
				return
			}
		}
		_, _ = w.Write(fixture)
	}))
	defer server.Close()
	client := New(Config{BaseURL: server.URL, Timeout: 5 * time.Second, Conns: 4})
	store := fake.NewStore()
	store.SeedTenant(participant.TenantState{TenantID: "t1", Status: participant.TenantActive})
	store.SeedParticipant(participant.State{
		TenantID: "t1",
		Stage:    participant.StageConnected,
		Enabled:  true,
		Delivery: participant.DeliverySettings{Regions: []string{"RU-AD"}, Carriers: []string{"cdek", "yandex"}},
	})
	guard := platformguard.New(
		client,
		platformguard.Config{
			ReadTimeout:           300 * time.Millisecond,
			StrictTimeout:         5 * time.Second,
			MaxConcurrent:         4,
			ReadMaxConcurrent:     2,
			CheckoutMaxConcurrent: 2,
			TenantMaxConcurrent:   2,
			TenantStateSize:       100,
			FailureWindow:         30 * time.Second,
			FailureThreshold:      5,
			FailureMinCalls:       20,
			FailurePercent:        50,
		},
		fake.NewClock(time.Now()),
		nil,
		nil,
	)
	service := appdelivery.NewService(store, guard, fake.NewPickupCache(), appdelivery.Config{
		Attempts:     3,
		QuoteTimeout: 5 * time.Second,
		TTL:          time.Hour,
	})
	started := time.Now()
	points, err := service.Search(context.Background(), "cdek", "Город & район")
	cold := time.Since(started)
	if err != nil || len(points) != 1 {
		t.Fatal(points, err)
	}
	started = time.Now()
	points, err = service.Search(context.Background(), "cdek", "Город & район")
	warm := time.Since(started)
	if err != nil || len(points) != 1 || calls.Load() != 1 || warm >= 250*time.Millisecond {
		t.Fatal("тёплый путь не должен обращаться к магазину", points, err, calls.Load(), warm)
	}
	t.Logf("город 3 с + список 3 с: cold=%v warm=%v HTTP cold=1 warm=0", cold, warm)
}
