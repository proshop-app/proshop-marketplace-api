//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/application/searchfeedback"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/searchapi"
)

type searchContractFixture struct {
	offset  int
	aligned atomic.Bool
	reject  atomic.Bool
	probes  atomic.Int32
}

func TestOutboxWaitsForSearchContractAcrossReplicas(t *testing.T) {
	for _, offset := range []int{-1, 1} {
		t.Run(fmt.Sprintf("смещение версии %+d", offset), func(t *testing.T) {
			checkSearchContractRollout(t, &searchContractFixture{offset: offset})
		})
	}
}

func checkSearchContractRollout(t *testing.T, search *searchContractFixture) {
	t.Helper()
	ctx := context.Background()
	clock := fake.NewClock(at(0))
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	seedRecommendationProducts(t, uow, "t1", 1)
	seedRecommendationProducts(t, uow, "t2", 1)
	server := httptest.NewServer(search)
	t.Cleanup(server.Close)
	first := newSearchOutageReplica(ctx, t, pool.Config(), clock, server.URL)
	second := newSearchOutageReplica(ctx, t, pool.Config(), clock, server.URL)
	queue := postgres.NewTaskQueue(postgres.Single(pool))
	add := func(tasks ...ports.NewTask) {
		t.Helper()
		if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			return tx.Outbox().EnqueueBatch(ctx, tasks)
		}); err != nil {
			t.Fatal(err)
		}
	}
	add(appcatalog.SyncOfferTask("t1", "p1", clock.Now()))

	const passes = messaging.DefaultMaxAttempts + 1
	for attempt := 1; attempt <= passes; attempt++ {
		outcomes := runSearchOutagePass(ctx, t, first)
		if len(outcomes) != 1 || !errors.Is(outcomes[0].Err, ports.ErrOfferContractMismatch) ||
			outcomes[0].FailureReason != "" {
			t.Fatalf("попытка %d: %v", attempt, outcomes)
		}
		if attempt < passes {
			clock.Advance(messaging.Backoff(attempt))
		}
	}

	for _, kind := range []string{searchfeedback.TaskPurchase, ports.TaskPurgeTenant, "settings"} {
		add(ports.NewTask{
			Kind:     kind,
			TenantID: "t2",
			Payload:  []byte("{}"),
			At:       clock.Now(),
		})
		got := runSearchOutagePass(ctx, t, second)
		if len(got) != 1 || got[0].Task.Kind != kind || got[0].Err != nil {
			t.Fatalf("вид %s должен выполниться сразу: %v", kind, got)
		}
	}

	search.aligned.Store(true)
	add(appcatalog.SyncOfferTask("t2", "p1", clock.Now()))
	got := runSearchOutagePass(ctx, t, second)
	if len(got) != 1 || got[0].Task.TenantID != "t2" || got[0].Err != nil {
		t.Fatalf("синхронизация второго магазина должна выполниться сразу: %v", got)
	}
	clock.Advance(messaging.Backoff(passes))
	got = runSearchOutagePass(ctx, t, first)
	if len(got) != 1 || got[0].Task.TenantID != "t1" || got[0].Err != nil {
		t.Fatalf("первая задача должна завершиться при повторе: %v", got)
	}
	if failures, err := queue.Failures(ctx, 0, 10); err != nil || len(failures) != 0 {
		t.Fatalf("несовместимость попала в карантин: %v, %v", failures, err)
	}

	search.reject.Store(true)
	add(appcatalog.SyncOfferTask("t1", "p1", clock.Now()))
	invalid := runSearchOutagePass(ctx, t, first)
	if len(invalid) != 1 || !errors.Is(invalid[0].Err, searchapi.ErrInvalid) {
		t.Fatalf("настоящий 400: %v", invalid)
	}
	if failures, err := queue.Failures(ctx, 0, 10); err != nil || len(failures) != 1 ||
		failures[0].Reason != "search.invalid_request" || failures[0].Attempts != 1 {
		t.Fatalf("настоящий 400 должен уйти в карантин при первом взятии: %v, %v", failures, err)
	}
	if probes := search.probes.Load(); probes != 0 {
		t.Fatalf("дополнительных HTTP-запросов: %d", probes)
	}
}

func (s *searchContractFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/api/v1/internal/marketplace/offers" {
		s.probes.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	var batch struct {
		Version int `json:"contractVersion"`
	}
	if err := json.NewDecoder(r.Body).Decode(&batch); err != nil || batch.Version <= 0 || s.reject.Load() {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"REQUEST_INVALID"}`))
		return
	}
	expected := batch.Version
	if !s.aligned.Load() {
		expected += s.offset
	}
	if batch.Version != expected {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"code":"CONTRACT_VERSION_MISMATCH"}`))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
