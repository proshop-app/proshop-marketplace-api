package identityhub

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

type blockingKeysTransport struct {
	next    http.RoundTripper
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

// panickingKeysTransport падает паникой на запросе ключей.
type panickingKeysTransport struct{}

func (panickingKeysTransport) RoundTrip(*http.Request) (*http.Response, error) {
	panic("сбой разбора ключей")
}

func (b *blockingKeysTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if b.calls.Add(1) == 1 {
		close(b.started)
	}
	select {
	case <-b.release:
		return b.next.RoundTrip(request)
	case <-request.Context().Done():
		return nil, request.Context().Err()
	}
}

func TestKnownKeysBypassSlowRefresh(t *testing.T) {
	f := newJWTFixture(t)
	f.client.keys["key-one"] = &f.key.PublicKey
	f.client.keysUntil = time.Now().Add(time.Minute)
	transport := &blockingKeysTransport{
		next:    http.DefaultTransport,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	f.client.http.Transport = transport
	unknown := make(chan error, 1)
	go func() {
		_, err := f.client.key(context.Background(), "new-key")
		unknown <- err
	}()
	<-transport.started
	known := make(chan error, 1)
	go func() {
		_, err := f.client.key(context.Background(), "key-one")
		known <- err
	}()
	select {
	case err := <-known:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Error("известный ключ ждёт чужой HTTP под общим замком")
	}
	close(transport.release)
	<-unknown
}

func TestRefreshCoalescesRotationAndAllowsWaiterCancellation(t *testing.T) {
	f := newJWTFixture(t)
	f.client.keys["old-key"] = &f.key.PublicKey
	transport := &blockingKeysTransport{
		next:    http.DefaultTransport,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	f.client.http.Transport = transport
	// Даже инициатор обновления просроченного кэша проверяет известный ключ сразу.
	if _, err := f.client.key(context.Background(), "old-key"); err != nil {
		t.Fatal(err)
	}
	<-transport.started
	ctx, cancel := context.WithCancel(context.Background())
	canceled := make(chan error, 1)
	go func() {
		_, err := f.client.key(ctx, "key-one")
		canceled <- err
	}()
	cancel()
	select {
	case err := <-canceled:
		if !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
	case <-time.After(time.Second):
		t.Error("ожидающий запрос не слушает отмену")
	}
	waiters := make(chan error, 20)
	for range 20 {
		go func() {
			_, err := f.client.key(context.Background(), "key-one")
			waiters <- err
		}()
	}
	close(transport.release)
	for range 20 {
		if err := <-waiters; err != nil {
			t.Fatal(err)
		}
	}
	if transport.calls.Load() != 1 {
		t.Fatal("одновременные обновления не объединены", transport.calls.Load())
	}
}

func TestPanickingKeyRefreshClosesFlight(t *testing.T) {
	f := newJWTFixture(t)
	f.client.http.Transport = panickingKeysTransport{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	if _, err := f.client.key(ctx, "new-key"); err == nil || time.Since(started) >= time.Second {
		t.Fatalf("паника обновления ключей закрывает рейс сразу: %v за %s", err, time.Since(started))
	}
	f.client.mu.Lock()
	defer f.client.mu.Unlock()
	if f.client.refresh != nil {
		t.Fatal("после паники новое обновление ключей может начаться")
	}
}
