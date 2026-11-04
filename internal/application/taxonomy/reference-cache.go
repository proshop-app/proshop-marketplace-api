package taxonomy

import (
	"context"
	"slices"
	"sort"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

// unknownRevision — словарь взят первым запросом до сверки ревизий:
// ближайшая сверка перечитает его один раз и запомнит ревизию владельца.
const unknownRevision int64 = -1

var errReferenceStale = errs.New(
	errs.Unavailable,
	"reference.values_stale",
	"значения схемы давно не сверялись с владельцем",
)

// ReferenceCache — производная копия закрытых словарей platform в памяти
// процесса. Схема читается без похода к владельцу: фоновая сверка ревизий
// перечитывает только изменившиеся словари. В копию попадает только ответ
// владельца, поэтому вторым источником истины она не становится.
type ReferenceCache struct {
	source  ports.ReferenceSource
	clock   ports.Clock
	maxAge  time.Duration
	mu      sync.RWMutex
	entries map[string]cachedValues
	loads   singleflight.Group
}

// ReferenceRefresh — итог одной сверки для журнала и метрик. Age — сколько
// не подтверждался самый давний словарь копии.
type ReferenceRefresh struct {
	Checked int
	Updated []string
	Dropped []string
	Failed  map[string]error
	Age     time.Duration
}

type cachedValues struct {
	items     []ports.ReferenceValue
	revision  int64
	confirmed time.Time
}

// lastingValues отдаёт последнюю полученную копию без срока давности.
type lastingValues struct {
	cache *ReferenceCache
}

// NewReferenceCache не обращается к сети: первая сверка идёт в фоновом цикле,
// запуск marketplace не ждёт сервер.
func NewReferenceCache(source ports.ReferenceSource, clock ports.Clock, maxAge time.Duration) *ReferenceCache {
	return &ReferenceCache{
		source:  source,
		clock:   clock,
		maxAge:  maxAge,
		entries: map[string]cachedValues{},
	}
}

// Values отдаёт копию, подтверждённую владельцем не раньше maxAge назад:
// проверка публикации не принимает значения, о которых владелец давно молчит.
func (c *ReferenceCache) Values(ctx context.Context, code string) ([]ports.ReferenceValue, error) {
	return c.values(ctx, code, c.maxAge)
}

// Lasting — представление для выдачи покупателю: пока владелец недоступен,
// фильтры и варианты строятся по последней полученной копии.
func (c *ReferenceCache) Lasting() ports.ReferenceValues {
	return lastingValues{cache: c}
}

// Refresh сверяет ревизии одним запросом и перечитывает изменившиеся закрытые
// словари. Сбой словаря оставляет прежнюю копию без нового подтверждения.
func (c *ReferenceCache) Refresh(ctx context.Context) (ReferenceRefresh, error) {
	revisions, err := c.source.Revisions(ctx)
	if err != nil {
		return ReferenceRefresh{Age: c.age()}, err
	}
	closed := map[string]int64{}
	for _, dictionary := range revisions {
		if dictionary.Kind == "closed" {
			closed[dictionary.Code] = dictionary.Revision
		}
	}
	codes := make([]string, 0, len(closed))
	for code := range closed {
		codes = append(codes, code)
	}
	sort.Strings(codes)

	result := ReferenceRefresh{Checked: len(codes), Dropped: c.drop(closed), Failed: map[string]error{}}
	for _, code := range codes {
		if c.confirm(code, closed[code]) {
			continue
		}
		if _, err := c.load(ctx, code, closed[code]); err != nil {
			result.Failed[code] = err
			continue
		}
		result.Updated = append(result.Updated, code)
	}
	result.Age = c.age()
	return result, nil
}

func (v lastingValues) Values(ctx context.Context, code string) ([]ports.ReferenceValue, error) {
	return v.cache.values(ctx, code, 0)
}

// values: нулевой maxAge — срок не проверяется. Словаря ещё нет — один
// синхронный запрос к владельцу, без запасного пустого ответа.
func (c *ReferenceCache) values(
	ctx context.Context,
	code string,
	maxAge time.Duration,
) ([]ports.ReferenceValue, error) {
	c.mu.RLock()
	entry, found := c.entries[code]
	c.mu.RUnlock()
	if !found {
		return c.load(ctx, code, unknownRevision)
	}
	if maxAge > 0 && c.clock.Now().Sub(entry.confirmed) > maxAge {
		return nil, errReferenceStale
	}
	return slices.Clone(entry.items), nil
}

// load читает словарь один раз на всех ждущих. Отмена запроса, начавшего
// чтение, не обрывает его для остальных: срок задаёт таймаут клиента.
func (c *ReferenceCache) load(ctx context.Context, code string, revision int64) ([]ports.ReferenceValue, error) {
	loaded, err, _ := c.loads.Do(code, func() (any, error) {
		items, err := c.source.Values(context.WithoutCancel(ctx), code)
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			return nil, errReferenceValues
		}
		items = slices.Clone(items)
		c.mu.Lock()
		c.entries[code] = cachedValues{items: items, revision: revision, confirmed: c.clock.Now()}
		c.mu.Unlock()
		return items, nil
	})
	if err != nil {
		return nil, err
	}
	return slices.Clone(loaded.([]ports.ReferenceValue)), nil
}

// confirm продлевает копию, если ревизия владельца не изменилась.
func (c *ReferenceCache) confirm(code string, revision int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, found := c.entries[code]
	if !found || entry.revision != revision {
		return false
	}
	entry.confirmed = c.clock.Now()
	c.entries[code] = entry
	return true
}

// drop убирает словари, которых у владельца нет или которые перестали быть закрытыми.
func (c *ReferenceCache) drop(closed map[string]int64) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var dropped []string
	for code := range c.entries {
		if _, found := closed[code]; !found {
			delete(c.entries, code)
			dropped = append(dropped, code)
		}
	}
	sort.Strings(dropped)
	return dropped
}

func (c *ReferenceCache) age() time.Duration {
	c.mu.RLock()
	defer c.mu.RUnlock()
	now := c.clock.Now()
	var oldest time.Duration
	for _, entry := range c.entries {
		oldest = max(oldest, now.Sub(entry.confirmed))
	}
	return oldest
}
