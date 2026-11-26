package platformguard

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestReviewCacheEvictsLeastRecentlyUsed(t *testing.T) {
	upstream := fake.NewPlatform()
	c := New(upstream, testConfig(), guardTestClock(), nil, nil)
	for _, product := range []ids.ProductID{"p1", "p2", "p3", "p1", "p4"} {
		_, _ = c.ProductReviews(context.Background(), "t1", product, 20, 0)
	}
	for _, product := range []ids.ProductID{"p1", "p3", "p4"} {
		key := reviewKey([]any{ids.TenantID("t1"), product, 20, 0})
		if c.cache[key] == nil {
			t.Fatal("потеряна свежая запись", product)
		}
	}
	key := reviewKey([]any{ids.TenantID("t1"), ids.ProductID("p2"), 20, 0})
	if c.cache[key] != nil || c.cacheLRU.Len() != c.cfg.CacheSize || len(c.cache) != c.cfg.CacheSize {
		t.Fatal("вытеснена не самая давно использованная запись")
	}
}

func TestReviewCacheLockDoesNotBlockAdmission(t *testing.T) {
	c := New(fake.NewPlatform(), testConfig(), guardTestClock(), nil, nil)
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	result, _ := c.request(tenantContext("t1"), checkoutClass)
	if result.reason != "" {
		t.Fatal(result.reason)
	}
	c.release(result.permit, nil)
}

func TestEvictedLoadDoesNotReplaceNewEntry(t *testing.T) {
	cfg := testConfig()
	cfg.CacheSize = 1
	cfg.ReadTimeout = time.Second
	cfg.TenantMaxConcurrent = 4
	c := New(fake.NewPlatform(), cfg, guardTestClock(), nil, nil)
	key := reviewKey([]any{"p1"})
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		cached(context.Background(), c, key, "", func(context.Context) (string, error) {
			close(started)
			<-release
			return "старый", nil
		})
	}()
	<-started
	other := reviewKey([]any{"p2"})
	cached(context.Background(), c, other, "", func(context.Context) (string, error) { return "другой", nil })
	cached(context.Background(), c, key, "", func(context.Context) (string, error) { return "новый", nil })
	close(release)
	<-done
	if c.cache[key].value != "новый" || len(c.cache) != 1 || c.cacheLRU.Len() != 1 {
		t.Fatal("поздний ответ возродил вытесненную запись")
	}
}

// BenchmarkReviewCacheFootprint измеряет удерживаемую память, включая карту и LRU, без сетевых буферов.
func BenchmarkReviewCacheFootprint(b *testing.B) {
	for _, count := range []int{1, 20, 100} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			page := ports.ReviewPage{Total: count}
			for range count {
				page.Items = append(page.Items, ports.Review{
					Author:    "Имя",
					Rating:    5,
					Text:      "Текст отзыва",
					CreatedAt: time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC),
				})
			}
			raw, err := json.Marshal(page)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportMetric(float64(len(raw)), "json-B/page")
			for range b.N {
				measureReviewCache(b, raw)
			}
		})
	}
}

func measureReviewCache(b *testing.B, raw []byte) {
	b.Helper()
	c := New(fake.NewPlatform(), testConfig(), guardTestClock(), nil, nil)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := 0; i < 33000; i++ {
		var page ports.ReviewPage
		if err := json.Unmarshal(raw, &page); err != nil {
			b.Fatal(err)
		}
		key := reviewKey([]any{i})
		c.cache[key] = &cacheEntry{
			value:  page,
			loaded: c.clock.Now(),
			node:   c.cacheLRU.PushBack(key),
		}
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	b.ReportMetric(float64(after.HeapAlloc-before.HeapAlloc)/33000, "heap-B/entry")
	runtime.KeepAlive(c)
}
