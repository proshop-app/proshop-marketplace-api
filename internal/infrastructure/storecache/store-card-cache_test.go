package storecache_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/storecache"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type memoryCache struct {
	mu      sync.Mutex
	now     time.Time
	values  map[string]cacheValue
	fail    bool
	lastTTL time.Duration
}

type cacheValue struct {
	raw     []byte
	expires time.Time
}

type sourceCards struct {
	ports.StoreCards
	version atomic.Int64
	calls   atomic.Int64
	mu      sync.Mutex
	name    string
	entered chan struct{}
	release chan struct{}
}

type sourceTx struct {
	ports.Tx
	cards *sourceCards
}

type sourceUOW struct{ cards *sourceCards }

func (c *memoryCache) Get(_ context.Context, key string) ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail {
		return nil, false, errors.New("Redis недоступен")
	}
	value, ok := c.values[key]
	return value.raw, ok && c.now.Before(value.expires), nil
}

func (c *memoryCache) Set(_ context.Context, key string, raw []byte, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastTTL = ttl
	if c.fail {
		return errors.New("Redis недоступен")
	}
	c.values[key] = cacheValue{raw: raw, expires: c.now.Add(ttl)}
	return nil
}

func (u sourceUOW) Do(ctx context.Context, fn func(context.Context, ports.Tx) error) error {
	return fn(ctx, sourceTx{cards: u.cards})
}

func (t sourceTx) StoreCards() ports.StoreCards { return t.cards }

func (c *sourceCards) CacheVersion(context.Context) (int64, error) { return c.version.Load(), nil }

func (c *sourceCards) List(ctx context.Context, _ string, limit, offset int) (ports.ShopPage, error) {
	c.calls.Add(1)
	c.mu.Lock()
	name := c.name
	entered, release := c.entered, c.release
	c.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return ports.ShopPage{}, ctx.Err()
		}
	}
	price := int64(12345)
	return ports.ShopPage{
		Items: []ports.ShopCard{{
			TenantID:   "t1",
			Slug:       "shop",
			Name:       name,
			FromPrice:  &price,
			Products:   []ports.StoreCardProduct{},
			Appearance: participant.DefaultCardAppearance(),
		}},
		Total:  1,
		Limit:  limit,
		Offset: offset,
	}, nil
}

func (c *sourceCards) BatchPublic(ctx context.Context, tenants []ids.TenantID) ([]ports.ShopCard, error) {
	page, err := c.List(ctx, "", len(tenants), 0)
	return page.Items, err
}

func cachedFixture() (*storecache.UnitOfWork, *sourceCards, *memoryCache) {
	source := &sourceCards{name: "Магазин"}
	source.version.Store(1)
	cache := &memoryCache{now: time.Unix(0, 0), values: map[string]cacheValue{}}
	return storecache.NewUnitOfWork(sourceUOW{cards: source}, cache), source, cache
}

func readPage(uow ports.UnitOfWork) (ports.ShopPage, error) {
	var page ports.ShopPage
	err := uow.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		var err error
		page, err = tx.StoreCards().List(ctx, "", 20, 0)
		return err
	})
	return page, err
}

func TestStoreCardCacheTwentyMinutesAndMoneyBoundary(t *testing.T) {
	uow, source, cache := cachedFixture()
	first, err := readPage(uow)
	if err != nil || *first.Items[0].FromPrice != 12345 {
		t.Fatal(first, err)
	}
	source.mu.Lock()
	source.name = "Обновлён"
	source.mu.Unlock()
	cache.mu.Lock()
	cache.now = cache.now.Add(20*time.Minute - time.Nanosecond)
	cache.mu.Unlock()
	page, err := readPage(uow)
	if err != nil || page.Items[0].Name != "Магазин" || source.calls.Load() != 1 {
		t.Fatal(page, err, source.calls.Load())
	}
	cache.mu.Lock()
	cache.now = cache.now.Add(time.Nanosecond)
	cache.mu.Unlock()
	page, err = readPage(uow)
	if err != nil || page.Items[0].Name != "Обновлён" || source.calls.Load() != 2 ||
		cache.lastTTL != 20*time.Minute {
		t.Fatal(page, err, source.calls.Load(), cache.lastTTL)
	}
	for _, value := range cache.values {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(value.raw, &raw); err != nil {
			t.Fatal(err)
		}
		if string(raw["subscribed"]) != "" {
			t.Fatal("персональные сведения в кэше", raw)
		}
	}
	page.Items[0].Name = "Мутация потребителя"
	page, err = readPage(uow)
	if err != nil || page.Items[0].Name != "Обновлён" {
		t.Fatal(page, err)
	}
}

func TestStoreCardCacheGenerationAndRedisFailure(t *testing.T) {
	uow, source, cache := cachedFixture()
	if _, err := readPage(uow); err != nil {
		t.Fatal(err)
	}
	source.mu.Lock()
	source.name = "Новый публичный магазин"
	source.mu.Unlock()
	source.version.Add(1)
	page, err := readPage(uow)
	if err != nil || page.Items[0].Name != "Новый публичный магазин" || source.calls.Load() != 2 {
		t.Fatal(page, err, source.calls.Load())
	}
	cache.mu.Lock()
	cache.fail = true
	cache.mu.Unlock()
	source.version.Add(1)
	source.mu.Lock()
	source.name = "После изменения при отказе Redis"
	source.mu.Unlock()
	page, err = readPage(uow)
	if err != nil || page.Items[0].Name != "После изменения при отказе Redis" {
		t.Fatal(page, err)
	}
	cache.mu.Lock()
	cache.fail = false
	cache.mu.Unlock()
	page, err = readPage(uow)
	if err != nil || page.Items[0].Name != "После изменения при отказе Redis" || source.calls.Load() != 4 {
		t.Fatal(page, err, source.calls.Load())
	}
}

func TestStoreCardCacheSingleFlightAndStaleGeneration(t *testing.T) {
	uow, source, _ := cachedFixture()
	source.entered = make(chan struct{}, 20)
	source.release = make(chan struct{})
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := readPage(uow); err != nil {
				t.Error(err)
			}
		}()
	}
	<-source.entered
	// Следующее поколение не должно читать результат уже выполнявшегося старого запроса.
	source.mu.Lock()
	source.name = "Новое поколение"
	source.mu.Unlock()
	source.version.Add(1)
	close(source.release)
	wg.Wait()
	source.mu.Lock()
	source.entered = nil
	source.release = nil
	source.mu.Unlock()
	page, err := readPage(uow)
	if err != nil || page.Items[0].Name != "Новое поколение" || source.calls.Load() > 2 {
		t.Fatal(page, err, source.calls.Load())
	}
}

func TestStoreCardCacheBatchPublicUsesSameGeneration(t *testing.T) {
	uow, source, _ := cachedFixture()
	for range 2 {
		if err := uow.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
			items, err := tx.StoreCards().BatchPublic(ctx, []ids.TenantID{"t1"})
			if len(items) != 1 {
				t.Fatal(items)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	if source.calls.Load() != 1 {
		t.Fatal(source.calls.Load())
	}
}

func TestStoreCardCacheCorruptTypedJSONFallsBack(t *testing.T) {
	uow, source, cache := cachedFixture()
	for _, raw := range []string{
		`null`,
		`{}`,
		`[]`,
		`{"items":"wrong"}`,
		`{"items":[],"total":0}`,
		`{"items":[{}],"total":1,"limit":20,"offset":0}`,
	} {
		if _, err := readPage(uow); err != nil {
			t.Fatal(err)
		}
		cache.mu.Lock()
		for key, value := range cache.values {
			value.raw = []byte(raw)
			cache.values[key] = value
		}
		cache.mu.Unlock()
		before := source.calls.Load()
		page, err := readPage(uow)
		if err != nil || len(page.Items) != 1 || page.Items[0].Name != "Магазин" || source.calls.Load() != before+1 {
			t.Fatal(raw, page, err, source.calls.Load())
		}
	}
}

func TestStoreCardCacheCanceledLeaderDoesNotCancelFollower(t *testing.T) {
	uow, source, _ := cachedFixture()
	source.entered = make(chan struct{}, 20)
	source.release = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	leader := make(chan error, 1)
	go func() {
		leader <- uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			_, err := tx.StoreCards().List(ctx, "", 20, 0)
			return err
		})
	}()
	<-source.entered
	follower := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		_, err := readPage(uow)
		follower <- err
	}()
	<-started
	cancel()
	if err := <-leader; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(source.release)
	if err := <-follower; err != nil {
		t.Fatal("отмена другого запроса отменила читателя", err)
	}
}
