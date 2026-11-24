package delivery

import (
	"container/list"
	"context"
	"encoding/json"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/safego"
)

func (s *Service) memoryCatalog(key string) *pickupCatalog {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if !now.Before(s.nextCleanup) {
		s.nextCleanup = now.Add(pickupCleanupInterval)
		safego.Go("delivery.pickup-cleanup", func() { s.cleanupCatalogs(now) })
	}
	return s.catalogAt(key, now)
}

// catalogAt вызывается под mu, в том числе перед постановкой холодной загрузки.
func (s *Service) catalogAt(key string, now time.Time) *pickupCatalog {
	element := s.catalogs[key]
	if element == nil {
		return nil
	}
	entry := element.Value.(pickupCacheEntry)
	if !entry.catalog.ExpiresAt.After(now) {
		s.removeCatalog(element)
		return nil
	}
	s.lru.MoveToFront(element)
	return entry.catalog
}

func (s *Service) readCatalog(ctx context.Context, key string) *pickupCatalog {
	raw, hit, err := s.cache.Get(ctx, key)
	if err != nil || !hit {
		return nil
	}
	var catalog pickupCatalog
	if json.Unmarshal(raw, &catalog) != nil || !catalog.ExpiresAt.After(time.Now()) {
		return nil
	}
	catalog.bytes = len(raw)
	if !catalog.Failed {
		catalog.index = delivery.NewPickupIndex(catalog.Points)
	}
	return &catalog
}

// remember вызывается под mu; запись и индекс после публикации неизменяемы.
func (s *Service) remember(key string, catalog *pickupCatalog) {
	if previous := s.catalogs[key]; previous != nil {
		s.removeCatalog(previous)
	}
	for s.lru.Len() > 0 && s.cacheBytes+catalog.bytes > pickupCacheMemoryBytes {
		s.removeCatalog(s.lru.Back())
	}
	s.catalogs[key] = s.lru.PushFront(pickupCacheEntry{key: key, catalog: catalog})
	s.cacheBytes += catalog.bytes
}

func (s *Service) removeCatalog(element *list.Element) {
	if s.cleanupCursor == element {
		s.cleanupCursor = element.Prev()
	}
	entry := element.Value.(pickupCacheEntry)
	s.cacheBytes -= entry.catalog.bytes
	delete(s.catalogs, entry.key)
	s.lru.Remove(element)
}

// Один фоновый проход ограничен пачкой: тёплый запрос не обходит тысячи городов под общей блокировкой.
func (s *Service) cleanupCatalogs(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cleanupCursor == nil {
		s.cleanupCursor = s.lru.Back()
	}
	for scanned := 0; scanned < pickupCleanupBatch && s.cleanupCursor != nil; scanned++ {
		current := s.cleanupCursor
		s.cleanupCursor = current.Prev()
		if !current.Value.(pickupCacheEntry).catalog.ExpiresAt.After(now) {
			s.removeCatalog(current)
		}
	}
}

// Redis и разбор большого снимка объединяются отдельно от очередей вызовов магазинов.
func (s *Service) sharedCacheRead(ctx context.Context, key string) *pickupCatalog {
	result := s.cacheReads.DoChan(key, safego.GuardValue("delivery.pickup-cache-read", func() (any, error) {
		if cached := s.memoryCatalog(key); cached != nil {
			return cached, nil
		}
		readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.config.QuoteTimeout)
		defer cancel()
		catalog := s.readCatalog(readCtx, key)
		if catalog != nil {
			s.mu.Lock()
			s.remember(key, catalog)
			s.mu.Unlock()
		}
		return catalog, nil
	}))
	select {
	case <-ctx.Done():
		return nil
	case answer := <-result:
		catalog, _ := answer.Val.(*pickupCatalog)
		return catalog
	}
}
