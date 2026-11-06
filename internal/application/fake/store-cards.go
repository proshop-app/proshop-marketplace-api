package fake

import (
	"context"
	"sort"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type storeCards struct{ d *data }

func (r storeCards) Lock(_ context.Context, _ ids.TenantID) error {
	return nil
}

func (r storeCards) Save(_ context.Context, record ports.StoreRecord) error {
	previous := r.d.storeCards[record.Header.TenantID]
	if previous.ProfileVersion > record.ProfileVersion {
		record.Header.Slogan, record.Card.Slogan = previous.Header.Slogan, previous.Card.Slogan
		record.Header.Logo, record.Card.Logo = previous.Header.Logo, previous.Card.Logo
		record.Summary.Hidden = previous.Summary.Hidden
		record.ProfileVersion = previous.ProfileVersion
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
			header.Showcase = append([]ports.ShopImage{}, record.Card.Showcase...)
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

func (r storeCards) UpdateProfile(_ context.Context, p *participant.Participant) error {
	record, ok := r.d.storeCards[p.TenantID()]
	if !ok {
		record.Header = ports.ShopHeader{TenantID: p.TenantID(), Slug: p.Slug(), Name: p.Name()}
		record.Summary = ports.StoreSummary{
			Categories: []ports.StoreCategory{},
			Showcase:   []ports.StoreProduct{},
			Hidden:     []participant.CardHidden{},
		}
	}
	profile := p.PublicProfile()
	record.Header.Slogan, record.Card.Slogan = profile.Slogan, profile.Slogan
	record.Header.Logo, record.Card.Logo = profile.Logo, profile.Logo
	record.Summary.Hidden = append([]participant.CardHidden{}, p.State().StoreCard.Hidden...)
	record.ProfileVersion = p.Version()
	r.d.storeCards[p.TenantID()] = record
	return nil
}
