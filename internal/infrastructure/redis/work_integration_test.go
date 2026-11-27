//go:build integration

package redis_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/redis"
)

func TestWorkSignalReachesEveryReplicaDuringDrain(t *testing.T) {
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("нужен адрес изолированного Redis в TEST_REDIS_URL")
	}
	client, err := redis.NewClient(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	accept := func(kind string) bool { return kind == "test.work" }
	var watches []ports.WorkWatch
	for range 2 {
		receiver := redis.NewWorkSignals(client, logger)
		watches = append(watches, receiver.Watch(accept))
		stop := startWorkSignals(ctx, t, receiver)
		t.Cleanup(stop)
		assertSignal(t, watches[len(watches)-1], "", time.Time{})
	}

	sender := redis.NewWorkSignals(client, logger)
	local := sender.Watch(accept)
	stop := startWorkSignals(ctx, t, sender)
	t.Cleanup(stop)
	assertSignal(t, local, "", time.Time{})

	at := time.Unix(100, 0).UTC()
	sender.Notify(ports.WorkHints{"test.work": at})
	// Локальное уведомление уже доступно, даже пока пачка ждёт публикации.
	if got := local.Take()["test.work"]; !got.Equal(at) {
		t.Fatalf("локальный срок %v, нужен %v", got, at)
	}
	stop()
	for _, watch := range watches {
		assertSignal(t, watch, "test.work", at)
	}

	at = at.Add(time.Second)
	sender.Notify(ports.WorkHints{"test.work": at})
	for _, watch := range watches {
		assertSignal(t, watch, "test.work", at)
	}
}

func startWorkSignals(parent context.Context, t *testing.T, signals *redis.WorkSignals) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := signals.Run(ctx); err != nil {
			t.Error(err)
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func assertSignal(t *testing.T, watch ports.WorkWatch, kind string, at time.Time) {
	t.Helper()
	limit := time.NewTimer(5 * time.Second)
	defer limit.Stop()
	select {
	case <-watch.Changed():
		hints := watch.Take()
		if got, ok := hints[kind]; !ok || len(hints) != 1 || !got.Equal(at) {
			t.Fatalf("сигнал %v, нужен %q до %v", hints, kind, at)
		}
	case <-limit.C:
		t.Fatal("нет сигнала")
	}
}
