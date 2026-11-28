//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/application/searchfeedback"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/searchapi"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const searchOutagePause = 30 * time.Second

func TestOutboxSearchOutageAcrossThreeReplicas(t *testing.T) {
	for _, tc := range []struct {
		name   string
		global bool
	}{
		{name: "частичный отказ"},
		{name: "общий отказ", global: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			clock := fake.NewClock(at(0))
			pool := testPool(t)
			uow := postgres.NewUnitOfWork(pool)
			for _, tenant := range []ids.TenantID{"t1", "t2", "t3"} {
				seedRecommendationProducts(t, uow, tenant, 2)
			}
			var probes atomic.Int32
			var writes atomic.Int32
			var recovered atomic.Bool
			search := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeSearchOutageResponse(w, r, tc.global, recovered.Load(), &probes, &writes)
			}))
			t.Cleanup(search.Close)
			health, err := search.Client().Get(search.URL + "/health")
			if err != nil {
				t.Fatal(err)
			}
			_ = health.Body.Close()
			if health.StatusCode != http.StatusOK {
				t.Fatal("поисковый процесс должен оставаться доступным")
			}
			replicas := make([]*messaging.IndependentLane, 3)
			for i := range replicas {
				replicas[i] = newSearchOutageReplica(ctx, t, pool.Config(), clock, search.URL)
			}
			add := func(tasks ...ports.NewTask) {
				t.Helper()
				err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
					return tx.Outbox().EnqueueBatch(ctx, tasks)
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			now := clock.Now()
			add(appcatalog.SyncOfferTask("t1", "p1", now), appcatalog.SyncOfferTask("t1", "p2", now))
			failed := runSearchOutagePass(ctx, t, replicas[0])
			if len(failed) != 2 || probes.Load() != 1 {
				t.Fatalf("две задачи должны дать одну проверку доступности: %v, проб %d", failed, probes.Load())
			}
			for _, outcome := range failed {
				if !errors.Is(outcome.Err, searchapi.ErrUnavailable) {
					t.Fatal(outcome)
				}
			}
			for i, task := range []ports.NewTask{
				appcatalog.SyncOfferTask("t2", "p1", now),
				appcatalog.SyncTenantTask("t3", now),
			} {
				add(task, ports.NewTask{
					Kind:     "settings",
					TenantID: task.TenantID,
					Payload:  []byte("{}"),
					At:       now,
				})
				outcomes := runSearchOutagePass(ctx, t, replicas[i+1])
				if len(outcomes) == 0 || outcomes[0].Task.Kind != "settings" || outcomes[0].Err != nil {
					t.Fatalf("общая задача магазина %s не выполнена: %v", task.TenantID, outcomes)
				}
				if tc.global {
					if len(outcomes) != 1 {
						t.Fatalf("индексация должна ждать общей паузы: %v", outcomes)
					}
					continue
				}
				if len(outcomes) != 2 || outcomes[1].Task.Kind != task.Kind ||
					outcomes[1].Task.TenantID != task.TenantID || outcomes[1].Err != nil {
					t.Fatalf("индексация здорового магазина %s должна пройти сразу: %v", task.TenantID, outcomes)
				}
			}
			if probes.Load() != 1 {
				t.Fatalf("повторных проб %d", probes.Load()-1)
			}
			expectedWrites := int32(3)
			if tc.global {
				expectedWrites = 1
			}
			if writes.Load() != expectedWrites {
				t.Fatalf("попыток записи %d, ожидалось %d", writes.Load(), expectedWrites)
			}
			if tc.global {
				recovered.Store(true)
				clock.Advance(searchOutagePause - time.Second)
				if got := runSearchOutagePass(ctx, t, replicas[1]); len(got) != 0 {
					t.Fatal("пауза закончилась раньше срока", got)
				}
				clock.Advance(time.Second)
				got := runSearchOutagePass(ctx, t, replicas[2])
				if len(got) != 4 {
					t.Fatal("после общей паузы должны выполниться четыре задачи", got)
				}
				for _, outcome := range got {
					if outcome.Err != nil {
						t.Fatal(outcome)
					}
				}
			}
		})
	}
}

func writeSearchOutageResponse(
	w http.ResponseWriter,
	r *http.Request,
	global, recovered bool,
	probes, writes *atomic.Int32,
) {
	switch r.URL.Path {
	case "/health":
		w.WriteHeader(http.StatusOK)
	case "/api/v1/internal/marketplace/write-availability":
		if r.Method != http.MethodGet || r.Header.Get("X-Admin-Id") != "uno-marketplace-integration-token-0001" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		probes.Add(1)
		if global && !recovered {
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	case "/api/v1/internal/marketplace/offers":
		writes.Add(1)
		var batch struct {
			Offers []struct {
				TenantID ids.TenantID `json:"tenantId"`
			} `json:"offers"`
		}
		if err := json.NewDecoder(r.Body).Decode(&batch); err != nil || len(batch.Offers) == 0 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		for _, offer := range batch.Offers {
			if !recovered && (global || offer.TenantID == "t1") {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"code":"CATALOG_UNAVAILABLE"}`))
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		// Готовность поколения может быть нарушена даже при отказе одной его части.
		w.WriteHeader(http.StatusServiceUnavailable)
	}
}

func newSearchOutageReplica(
	ctx context.Context,
	t *testing.T,
	cfg *pgxpool.Config,
	clock ports.Clock,
	address string,
) *messaging.IndependentLane {
	t.Helper()
	index, err := searchapi.New(searchapi.Config{
		URL:   address,
		Token: "uno-marketplace-integration-token-0001",
	})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	indexer := appcatalog.NewIndexer(postgres.NewUnitOfWork(pool), index, clock, 100, nil, nil, time.Hour)
	dispatcher := messaging.NewDispatcher(
		postgres.NewTaskQueue(postgres.Single(pool)),
		clock,
		messaging.DispatcherConfig{Batch: 4, PerTenant: 4},
		map[string]messaging.Handler{
			appcatalog.TaskSyncTenant:   indexer.HandleTask,
			searchfeedback.TaskPurchase: func(context.Context, ports.Task) error { return nil },
			ports.TaskPurgeTenant:       func(context.Context, ports.Task) error { return nil },
			"settings":                  func(context.Context, ports.Task) error { return nil },
		},
	)
	dispatcher.RegisterBatch(appcatalog.TaskSyncOffer, indexer.HandleOfferBatch)
	return messaging.NewIndependentLane(
		dispatcher,
		ports.TaskLaneGate{
			Key: "offer-index",
			Kinds: []string{
				appcatalog.TaskSyncOffer,
				appcatalog.TaskSyncTenant,
				ports.TaskPurgeTenant,
				searchfeedback.TaskPurchase,
			},
		},
		searchOutagePause,
		func(err error) bool { return errors.Is(err, searchapi.ErrUnavailable) },
		index.ConfirmOutage,
	)
}

func runSearchOutagePass(ctx context.Context, t *testing.T, lane *messaging.IndependentLane) []messaging.Outcome {
	t.Helper()
	outcomes, err := runLaneForTest(ctx, lane)
	if err != nil {
		t.Fatal(err)
	}
	lane.Wait()
	indexed, err := runLaneForTest(ctx, lane)
	if err != nil {
		t.Fatal(err)
	}
	return append(outcomes, indexed...)
}

func runLaneForTest(ctx context.Context, lane *messaging.IndependentLane) ([]messaging.Outcome, error) {
	previous, previousErr := lane.Finish()
	outcomes, _, _, err := lane.RunWork(ctx, nil)
	return append(previous, outcomes...), errors.Join(previousErr, err)
}
