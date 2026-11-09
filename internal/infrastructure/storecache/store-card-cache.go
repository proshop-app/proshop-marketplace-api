// Package storecache кэширует публичные списки карточек, не сохраняя персональные ответы.
package storecache

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const cardCacheTTL = 20 * time.Minute

// UnitOfWork сохраняет транзакции источника; Redis используется только для публичного чтения.
type UnitOfWork struct {
	source  ports.UnitOfWork
	cache   ports.StoreCardCache
	flights singleflight.Group
}

type scope struct {
	ports.Tx
	owner *UnitOfWork
}

type cards struct {
	ports.StoreCards
	owner *UnitOfWork
}

// NewUnitOfWork оборачивает только публичные List и BatchPublic; остальные порты не меняются.
func NewUnitOfWork(source ports.UnitOfWork, cache ports.StoreCardCache) *UnitOfWork {
	return &UnitOfWork{source: source, cache: cache}
}

// Do сохраняет исходные commit/rollback и оборачивает только чтение карточек.
func (u *UnitOfWork) Do(ctx context.Context, fn func(context.Context, ports.Tx) error) error {
	return u.source.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return fn(ctx, scope{Tx: tx, owner: u})
	})
}

func (s scope) StoreCards() ports.StoreCards {
	return cards{StoreCards: s.Tx.StoreCards(), owner: s.owner}
}

func (r cards) List(ctx context.Context, category string, limit, offset int) (ports.ShopPage, error) {
	args := []any{category, limit, offset}
	valid := func(raw []byte) bool {
		var page ports.ShopPage
		return json.Unmarshal(raw, &page) == nil && page.Items != nil &&
			page.Limit == limit && page.Offset == offset && page.Total >= len(page.Items) && validCards(page.Items)
	}
	raw, err := r.read(ctx, "list", args, valid, func() (any, error) {
		return r.StoreCards.List(ctx, category, limit, offset)
	})
	var result ports.ShopPage
	if err == nil {
		err = json.Unmarshal(raw, &result)
	}
	return result, err
}

func (r cards) BatchPublic(ctx context.Context, tenants []ids.TenantID) ([]ports.ShopCard, error) {
	valid := func(raw []byte) bool {
		var items []ports.ShopCard
		return json.Unmarshal(raw, &items) == nil && items != nil && validCards(items)
	}
	raw, err := r.read(ctx, "batch", tenants, valid, func() (any, error) {
		return r.StoreCards.BatchPublic(ctx, tenants)
	})
	var result []ports.ShopCard
	if err == nil {
		err = json.Unmarshal(raw, &result)
	}
	return result, err
}

func (r cards) read(
	ctx context.Context,
	kind string,
	args any,
	valid func([]byte) bool,
	source func() (any, error),
) ([]byte, error) {
	version, err := r.CacheVersion(ctx)
	if err != nil {
		return r.load(source)
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("v1:%d:%s:%x", version, kind, sha256.Sum256(encoded))
	if raw, hit, err := r.cached(ctx, key, valid); err != nil {
		return r.load(source)
	} else if hit {
		return raw, nil
	}
	result, err, _ := r.owner.flights.Do(key, func() (any, error) {
		if raw, hit, err := r.cached(ctx, key, valid); err != nil {
			return r.load(source)
		} else if hit {
			return raw, nil
		}
		raw, err := r.load(source)
		if err != nil {
			return nil, err
		}
		// Старый запрос пишет только в своё поколение: следующий GET уже выберет новый ключ.
		_ = r.owner.cache.Set(ctx, key, raw, cardCacheTTL)
		return raw, nil
	})
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		// Отмена первого запроса не должна отменять независимых ожидающих читателей.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return r.load(source)
		}
		return nil, err
	}
	return result.([]byte), nil
}

func (r cards) cached(ctx context.Context, key string, valid func([]byte) bool) ([]byte, bool, error) {
	raw, hit, err := r.owner.cache.Get(ctx, key)
	if err != nil || !hit || !valid(raw) {
		return nil, false, err
	}
	return raw, true, nil
}

func (r cards) load(source func() (any, error)) ([]byte, error) {
	result, err := source()
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

func validCards(items []ports.ShopCard) bool {
	for _, card := range items {
		if card.TenantID == "" || card.Slug == "" || card.Name == "" || card.Products == nil ||
			card.Appearance.Color == "" || len(card.Products) > 3 {
			return false
		}
	}
	return true
}
