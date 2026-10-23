package jobs_test

import (
	"context"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/jobs"
)

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
