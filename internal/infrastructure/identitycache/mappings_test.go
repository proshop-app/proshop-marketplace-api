package identitycache

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
)

type countedMappings struct {
	finds atomic.Int32
	bound string
}

func (s *countedMappings) Bind(_ context.Context, subject, id string) error {
	s.bound = subject + ":" + id
	return buyer.ErrSessionNotFound
}

func (s *countedMappings) Find(_ context.Context, subject string) (string, error) {
	s.finds.Add(1)
	switch subject {
	case "missing":
		return "", buyer.ErrSessionNotFound
	case "empty":
		return "", nil
	default:
		return "id-" + subject, nil
	}
}

func TestMappingsTTL(t *testing.T) {
	clock := fake.NewClock(time.Now())
	source := &countedMappings{}
	cache := NewMappings(source, clock)
	ctx := context.Background()

	assertMapping(t, cache, "subject")
	clock.Advance(mappingTTL - time.Nanosecond)
	assertMapping(t, cache, "subject")
	if got := source.finds.Load(); got != 1 {
		t.Fatalf("чтений до истечения TTL: %d", got)
	}

	clock.Advance(time.Nanosecond)
	assertMapping(t, cache, "subject")
	assertMapping(t, cache, "subject")
	if got := source.finds.Load(); got != 2 {
		t.Fatalf("чтений после истечения TTL: %d", got)
	}
	if err := cache.Bind(ctx, "subject", "other"); !errors.Is(err, buyer.ErrSessionNotFound) ||
		source.bound != "subject:other" {
		t.Fatalf("Bind не делегирован: %q, %v", source.bound, err)
	}
	assertMapping(t, cache, "subject")
}

func TestMappingsDoesNotCacheMissingOrFailedFind(t *testing.T) {
	source := &countedMappings{}
	cache := NewMappings(source, fake.NewClock(time.Now()))
	ctx := context.Background()
	for range 2 {
		if id, err := cache.Find(ctx, "missing"); id != "" || !errors.Is(err, buyer.ErrSessionNotFound) {
			t.Fatalf("отсутствующая привязка: %q, %v", id, err)
		}
		if id, err := cache.Find(ctx, "empty"); id != "" || err != nil {
			t.Fatalf("пустая привязка: %q, %v", id, err)
		}
	}
	if got := source.finds.Load(); got != 4 || len(cache.entries) != 0 {
		t.Fatalf("чтений: %d, записей в кэше: %d", got, len(cache.entries))
	}
}

func TestMappingsEvictionAndConcurrentLimit(t *testing.T) {
	source := &countedMappings{}
	cache := NewMappings(source, fake.NewClock(time.Now()))
	for i := range mappingLimit {
		assertMapping(t, cache, strconv.Itoa(i))
	}
	assertMapping(t, cache, "overflow")
	assertMapping(t, cache, "1")
	assertMapping(t, cache, "0")
	if got := source.finds.Load(); got != mappingLimit+2 {
		t.Fatalf("ожидалось вытеснение первой записи: %d чтений", got)
	}

	var group sync.WaitGroup
	for worker := range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			for i := range 32 {
				_, _ = cache.Find(context.Background(), strconv.Itoa(mappingLimit+worker*32+i))
				_, _ = cache.Find(context.Background(), "shared")
			}
		}()
	}
	group.Wait()
	if len(cache.entries) != mappingLimit || len(cache.subjects) != mappingLimit {
		t.Fatalf("после гонки кэш содержит %d записей и %d позиций", len(cache.entries), len(cache.subjects))
	}
}

func assertMapping(t *testing.T, cache *Mappings, subject string) {
	t.Helper()
	if id, err := cache.Find(context.Background(), subject); err != nil || id != "id-"+subject {
		t.Fatalf("привязка %q: %q, %v", subject, id, err)
	}
}
