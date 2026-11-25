//go:build integration

package postgres_test

import (
	"context"
	"sync"
	"testing"
	"time"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
)

const (
	// resumePurchases — созревшие покупки одного прохода восстановления.
	resumePurchases = 20
	// resumeStatements — проход одной страницей: BEGIN, взятие покупок, их группы, задачи опроса, COMMIT.
	resumeStatements = 5
	countPollTasks   = `SELECT count(*), count(DISTINCT dedup_key) FROM outbox WHERE kind = $1`
)

// Проход восстановления берёт созревшие покупки страницей в одной транзакции: число SQL
// не растёт с числом покупок, задачи опроса ставятся одной пачкой.
func TestResumeSQLDoesNotGrowWithPurchases(t *testing.T) {
	bench := newOrderEventBench(t)
	ctx := context.Background()
	for n := range resumePurchases {
		bench.orderedPurchase(t, 100+n)
	}
	bench.clock.Advance(2 * time.Hour)
	bench.counter.reset()
	if err := bench.service.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	bench.counter.log(t, "resume")
	if got := bench.counter.total(); got != resumeStatements {
		t.Fatalf("проход на %d покупок: SQL %d, ожидалось %d", resumePurchases, got, resumeStatements)
	}
	bench.assertPollTasks(t, resumePurchases*len(orderEventShops))
}

// Реплики, оказавшиеся в проходе одновременно, не делают работу дважды и не конфликтуют:
// занятую покупку соседний проход пропускает.
func TestConcurrentResumePassesDoNotConflict(t *testing.T) {
	bench := newOrderEventBench(t)
	ctx := context.Background()
	for n := range resumePurchases {
		bench.orderedPurchase(t, 200+n)
	}
	bench.clock.Advance(2 * time.Hour)
	start := make(chan struct{})
	failures := make(chan error, 4)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			failures <- bench.service.Resume(ctx)
		}()
	}
	close(start)
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal("параллельный проход не должен падать на конфликте версии", err)
		}
	}
	bench.assertPollTasks(t, resumePurchases*len(orderEventShops))
}

func (b *orderEventBench) assertPollTasks(t *testing.T, want int) {
	t.Helper()
	var total, distinct int
	err := b.pool.QueryRow(context.Background(), countPollTasks, appcheckout.TaskPollOrder).Scan(&total, &distinct)
	if err != nil {
		t.Fatal(err)
	}
	if total != want || distinct != want {
		t.Fatalf("задачи опроса: %d (разных %d), ожидалось %d", total, distinct, want)
	}
}
