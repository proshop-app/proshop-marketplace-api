package safego_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/safego"
)

func TestRunDeliversPanicToWaiterAndReporter(t *testing.T) {
	var mu sync.Mutex
	reported := map[string]error{}
	safego.SetReporter(func(name string, err error) {
		mu.Lock()
		defer mu.Unlock()
		reported[name] = err
	})
	defer safego.SetReporter(nil)

	done := make(chan error, 1)
	safego.Run("test.flight", func() error { panic("сбой загрузки") }, func(err error) { done <- err })
	select {
	case err := <-done:
		if !errors.Is(err, errs.ErrPanic) {
			t.Fatalf("ожидающий получает панику ошибкой: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("рейс остался открытым после паники ведущего")
	}
	mu.Lock()
	defer mu.Unlock()
	if _, ok := errs.PanicStack(reported["test.flight"]); !ok {
		t.Fatalf("паника ушла получателю со стеком: %v", reported)
	}
}

func TestRunPassesOrdinaryResultAndGoRunsDefers(t *testing.T) {
	failure := errors.New("ошибка")
	done := make(chan error, 1)
	safego.Run("test.result", func() error { return failure }, func(err error) { done <- err })
	if err := <-done; !errors.Is(err, failure) {
		t.Fatal(err)
	}

	safego.SetReporter(func(string, error) {})
	defer safego.SetReporter(nil)
	var wg sync.WaitGroup
	wg.Add(1)
	safego.Go("test.defer", func() {
		defer wg.Done()
		panic("сбой")
	})
	wg.Wait()
}

func TestGuardTurnsPanicOfForeignLauncherIntoError(t *testing.T) {
	safego.SetReporter(func(string, error) {})
	defer safego.SetReporter(nil)
	if err := safego.Guard("test.group", func() error { panic("сбой") })(); !errors.Is(err, errs.ErrPanic) {
		t.Fatalf("errgroup получает панику ошибкой: %v", err)
	}
	value, err := safego.GuardValue("test.flight", func() (any, error) { panic("сбой") })()
	if value != nil || !errors.Is(err, errs.ErrPanic) {
		t.Fatalf("рейс singleflight получает панику ошибкой: %v %v", value, err)
	}
}
