package fake

import (
	"context"
	"sort"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type storeCards struct{ d *data }

type storeDirectory struct {
	revision int64
	changes  map[ids.TenantID]directoryChange
}

type directoryChange struct {
	revision int64
	card     ports.ShopDirectoryCard
}

func (r storeCards) CacheVersion(context.Context, ...ids.TenantID) (int64, error) { return 0, nil }

func (r storeCards) Lock(_ context.Context, _ ids.TenantID) error {
	return nil
}

func (r storeCards) Save(_ context.Context, record ports.StoreRecord) error {
	previous := r.d.storeCards[record.Header.TenantID]
	if previous.ProfileVersion > record.ProfileVersion {
		record.Header.Logo, record.Card.Logo = previous.Header.Logo, previous.Card.Logo
		record.Card.Mark = previous.Card.Mark
		record.Summary.Hidden = previous.Summary.Hidden
		record.ProfileVersion = previous.ProfileVersion
	}
	before, after := directoryCard(previous), directoryCard(record)
	if before != after {
		r.d.directory.revision++
		r.d.directory.changes[record.Header.TenantID] = directoryChange{
			revision: r.d.directory.revision,
			card:     after,
		}
	}
	r.d.storeCards[record.Header.TenantID] = record
	return nil
}

func (r storeCards) Get(_ context.Context, tenant ids.TenantID) (ports.StoreRecord, error) {
	record, ok := r.d.storeCards[tenant]
	if !ok {
		return record, participant.ErrNotFound
	}
	return record, nil
}

func (r storeCards) Shop(_ context.Context, slug string) (ports.ShopHeader, error) {
	for _, record := range r.d.storeCards {
		if record.PagePublic && record.Header.Slug == slug {
			header := record.Header
			return header, nil
		}
	}
	return ports.ShopHeader{}, participant.ErrNotFound
}

func (r storeCards) List(_ context.Context, category string, limit, offset int) (ports.ShopPage, error) {
	result := ports.ShopPage{Items: []ports.ShopCard{}, Limit: limit, Offset: offset}
	for _, record := range r.d.storeCards {
		if !record.Public {
			continue
		}
		card := record.Card
		if category != "" {
			c, ok := record.Summary.CategoryCounts[category]
			if !ok || c.Count == 0 {
				continue
			}
			card.Offers = c.Count
			card.Category = &ports.ShopCategory{ID: c.ID, Name: c.Name}
		}
		result.Items = append(result.Items, card)
	}
	sort.Slice(result.Items, func(i, j int) bool {
		a, b := result.Items[i], result.Items[j]
		if a.Offers != b.Offers {
			return a.Offers > b.Offers
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.TenantID < b.TenantID
	})
	result.Total = len(result.Items)
	start := min(offset, result.Total)
	result.Items = result.Items[start:min(start+limit, result.Total)]
	return result, nil
}

func directoryCard(record ports.StoreRecord) ports.ShopDirectoryCard {
	if !record.Public {
		return ports.ShopDirectoryCard{}
	}
	return ports.ShopDirectoryCard{
		TenantID: record.Header.TenantID,
		Slug:     record.Header.Slug,
		Name:     record.Header.Name,
		SizeTier: shopSizeTier(record.Header.Offers),
	}
}

func shopSizeTier(offers int) int {
	switch {
	case offers >= 100:
		return 3
	case offers >= 10:
		return 2
	case offers >= 1:
		return 1
	default:
		return 0
	}
}

func (r storeCards) Directory(_ context.Context, previous int64) (ports.ShopDirectory, error) {
	result := ports.ShopDirectory{Revision: r.d.directory.revision}
	if result.Revision == previous {
		return result, nil
	}
	result.Items = []ports.ShopDirectoryCard{}
	result.Removed = []ids.TenantID{}
	for tenant, change := range r.d.directory.changes {
		if change.revision <= previous {
			continue
		}
		if change.card.TenantID != "" {
			result.Items = append(result.Items, change.card)
		} else if previous != 0 {
			result.Removed = append(result.Removed, tenant)
		}
	}
	return result, nil
}

func (r storeCards) UpdateProfile(_ context.Context, p *participant.Participant) error {
	record, ok := r.d.storeCards[p.TenantID()]
	if !ok {
		record.Header = ports.ShopHeader{TenantID: p.TenantID(), Slug: p.Slug(), Name: p.Name()}
		record.Summary = ports.StoreSummary{
			Categories: []ports.StoreCategory{},
			Hidden:     []participant.CardHidden{},
		}
	}
	profile := p.PublicProfile()
	record.Header.Logo, record.Card.Logo, record.Card.Mark = profile.Logo, profile.Logo, profile.Mark
	record.Summary.Hidden = append([]participant.CardHidden{}, p.State().StoreCard.Hidden...)
	record.ProfileVersion = p.Version()
	r.d.storeCards[p.TenantID()] = record
	return nil
}

// BatchPublic возвращает публичные карточки выбранных магазинов.
func (r storeCards) BatchPublic(_ context.Context, tenants []ids.TenantID) ([]ports.ShopCard, error) {
	result := []ports.ShopCard{}
	for _, tenant := range tenants {
		if record := r.d.storeCards[tenant]; record.PagePublic {
			result = append(result, record.Card)
		}
	}
	return result, nil
}
