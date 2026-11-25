package jobs_test

import (
	"context"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/jobs"
)

type busySchedule struct{}

func TestOrderRecoveryRunsAtStartupAndOnSchedule(t *testing.T) {
	probe := &fake.OrderRecovery{Calls: make(chan struct{}, 2)}
	runner := jobs.NewOrdersRunner(probe, 5*time.Millisecond, quiet)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	for range 2 {
		select {
		case <-probe.Calls:
		case <-time.After(time.Second):
			t.Fatal("не было стартового или планового прохода")
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("цикл не остановился")
	}
}

// Интервал взяла другая реплика: эта не проходит покупки и не пишет это в журнал как сбой.
func TestOrderRecoverySkipsIntervalOfOtherReplica(t *testing.T) {
	probe := &fake.OrderRecovery{Calls: make(chan struct{}, 1)}
	busy := busySchedule{}
	runner := jobs.NewOrdersRunner(probe, time.Millisecond, quiet).WithSchedule(busy)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-probe.Calls:
		t.Fatal("проход без взятого интервала")
	default:
	}
}

func (busySchedule) WithLock(context.Context, func(context.Context) error) error {
	return ports.ErrRebuildBusy
}
