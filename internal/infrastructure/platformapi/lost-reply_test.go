package platformapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/platformguard"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/platformapi"
)

type admissionClock struct {
	ports.Clock
	pause   atomic.Bool
	entered chan struct{}
	resume  chan struct{}
}

type lostReplyShop struct {
	t             *testing.T
	mu            sync.Mutex
	bodies        map[string][]byte
	attempts      int
	reservations  int
	order         []byte
	quote         []byte
	quoteEntered  chan struct{}
	quoteRelease  chan struct{}
	firstFinished chan struct{}
}

func (c *admissionClock) Now() time.Time {
	if c.pause.CompareAndSwap(true, false) {
		close(c.entered)
		<-c.resume
	}
	return c.Clock.Now()
}

func newLostReplyShop(t *testing.T) *lostReplyShop {
	t.Helper()
	order, err := os.ReadFile("testdata/order_created.json")
	if err != nil {
		t.Fatal(err)
	}
	quote, err := os.ReadFile("testdata/delivery_quote_available.json")
	if err != nil {
		t.Fatal(err)
	}
	return &lostReplyShop{
		t:             t,
		bodies:        make(map[string][]byte),
		order:         order,
		quote:         quote,
		quoteEntered:  make(chan struct{}),
		quoteRelease:  make(chan struct{}),
		firstFinished: make(chan struct{}),
	}
}

func (s *lostReplyShop) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case prefix + "/delivery/quote":
		close(s.quoteEntered)
		select {
		case <-s.quoteRelease:
		case <-r.Context().Done():
		}
		_, _ = w.Write(s.quote)
	case prefix + "/orders":
		first := s.commit(r)
		if first {
			// Резерв уже записан. Отключение клиента не отменяет завершённую транзакцию магазина.
			<-r.Context().Done()
			defer close(s.firstFinished)
		}
		_, _ = w.Write(s.order)
	default:
		s.t.Errorf("неожиданный вызов: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func (s *lostReplyShop) commit(r *http.Request) bool {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		s.t.Error(err)
	}
	var key struct {
		ExternalCheckoutID string `json:"externalCheckoutId"`
		GroupID            string `json:"groupId"`
	}
	if err := json.Unmarshal(raw, &key); err != nil {
		s.t.Error(err)
	}
	if key.ExternalCheckoutID != "checkout1:t1" || key.GroupID != "t1" {
		s.t.Error("потерян ключ оформления", key)
	}
	identity := r.URL.Path + ":" + key.ExternalCheckoutID + ":" + key.GroupID
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts++
	previous, exists := s.bodies[identity]
	if exists && !bytes.Equal(previous, raw) {
		s.t.Error("повтор отправил другой снимок")
	}
	if !exists {
		s.bodies[identity] = raw
		s.reservations++
	}
	return s.attempts == 1
}

func lostReplyGuard(
	upstream ports.PlatformAPI,
	clock ports.Clock,
	metrics ports.PlatformMetrics,
) *platformguard.Client {
	return platformguard.New(
		upstream,
		platformguard.Config{
			StrictTimeout:         200 * time.Millisecond,
			MaxConcurrent:         2,
			CheckoutMaxConcurrent: 1,
			ReadMaxConcurrent:     1,
			TenantMaxConcurrent:   2,
			QueueSize:             4,
			TenantQueueSize:       2,
			TenantStateSize:       10,
			TenantIdleTTL:         time.Minute,
			FailureThreshold:      5,
			FailureWindow:         30 * time.Second,
			FailureMinCalls:       20,
			FailurePercent:        50,
			OpenInterval:          time.Second,
		},
		clock,
		metrics,
		nil,
	)
}

func TestQueuedCreationLostReplyReturnsSameOrderOnRetry(t *testing.T) {
	shop := newLostReplyShop(t)
	server := httptest.NewServer(shop)
	defer server.Close()
	upstream := platformapi.New(platformapi.Config{BaseURL: server.URL, Timeout: time.Second, Conns: 4})
	clock := &admissionClock{
		Clock:   fake.NewClock(time.Now()),
		entered: make(chan struct{}),
		resume:  make(chan struct{}),
	}
	metrics := &fake.PlatformMetrics{}
	client := lostReplyGuard(upstream, clock, metrics)
	loseQueuedCreation(t, client, clock, shop)
	order, err := client.CreateOrder(context.Background(), "t1", orderRequest())
	if err != nil || order.OrderID != "o1" || metrics.QueueCount("checkout", "admitted") != 1 {
		t.Fatal("ожидание или повтор изменили заказ", order, err)
	}
	shop.mu.Lock()
	defer shop.mu.Unlock()
	if shop.attempts != 2 || len(shop.bodies) != 1 || shop.reservations != 1 {
		t.Fatal("повтор создал второй заказ или резерв", shop.attempts, len(shop.bodies), shop.reservations)
	}
}

func loseQueuedCreation(t *testing.T, client *platformguard.Client, clock *admissionClock, shop *lostReplyShop) {
	t.Helper()
	resume := sync.OnceFunc(func() { close(clock.resume) })
	defer resume()
	releaseQuote := sync.OnceFunc(func() { close(shop.quoteRelease) })
	defer releaseQuote()
	quoteDone := make(chan error, 1)
	go func() {
		_, err := client.QuoteDelivery(context.Background(), "t1", ports.QuoteRequest{})
		quoteDone <- err
	}()
	<-shop.quoteEntered
	clock.pause.Store(true)
	createDone := make(chan error, 1)
	go func() {
		_, err := client.CreateOrder(context.Background(), "t1", orderRequest())
		createDone <- err
	}()
	// acquire держит mutex: возврат места quote произойдёт только после постановки create в очередь.
	<-clock.entered
	releaseQuote()
	resume()
	if err := <-createDone; !errors.Is(err, platformguard.ErrTimeout) {
		t.Fatal("первый ответ должен потеряться после создания", err)
	}
	if err := <-quoteDone; err != nil {
		t.Fatal(err)
	}
	shop.mu.Lock()
	attempts := shop.attempts
	shop.mu.Unlock()
	if attempts != 1 {
		t.Fatal("запрос истёк до отправки на сервер", attempts)
	}
	<-shop.firstFinished
}
