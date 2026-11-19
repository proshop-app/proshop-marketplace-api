package participant

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"hash"
	"sort"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
)

type storeTotals struct {
	offers     int
	fromPrice  *int64
	categories map[string]StoreCategory
}

type storeSummaryBuilder struct {
	visible         storeTotals
	candidates      storeTotals
	best            []storeOffer
	paths           map[string][]StoreCategory
	roots           map[string]StoreCategory
	signature       hash.Hash
	lastRatingGroup string
	weighted        float64
	ratings         int64
	uncategorized   int
}

type storeProductSignature struct {
	Product    ports.ShopCardProduct
	CategoryID string
}

func summarizeStore(ctx context.Context, tx ports.Tx, p *participant.Participant) (StoreSummary, error) {
	definition, err := tx.Taxonomy().Current(ctx)
	if err != nil {
		return StoreSummary{}, err
	}
	builder := storeSummaryBuilder{
		visible:    storeTotals{categories: map[string]StoreCategory{}},
		candidates: storeTotals{categories: map[string]StoreCategory{}},
		paths:      categoryPaths(definition.Categories),
		roots:      categoryRoots(definition.Categories),
		signature:  sha256.New(),
	}
	if err := builder.read(ctx, tx, p); err != nil {
		return StoreSummary{}, err
	}
	return builder.summary(p)
}

func (b *storeSummaryBuilder) read(ctx context.Context, tx ports.Tx, p *participant.Participant) error {
	return tx.Offers().WalkStoreSummaryOffers(ctx, p.TenantID(), func(states []ports.OfferState) error {
		for _, state := range states {
			if err := b.add(state, p); err != nil {
				return err
			}
		}
		return nil
	})
}

func (b *storeSummaryBuilder) add(state ports.OfferState, p *participant.Participant) error {
	product := state.Product.State()
	if product.Deleted || product.TenantID != p.TenantID() {
		return nil
	}
	if card := product.Card.Marketplace; card != nil && card.Published && b.roots[card.CategoryID].ID == "" {
		b.uncategorized++
	}
	if _, ok := catalog.SelectPrice(product.PriceProfiles, catalog.DefaultPriceProfile); !ok {
		return nil
	}
	visibility := participant.VisibilityOf(p, state.Tenant)
	seller := catalog.Seller{
		Slug:              p.Slug(),
		Name:              p.Name(),
		Visible:           visibility.Visible,
		CheckoutAvailable: visibility.CheckoutAvailable,
	}
	offer := catalog.ComposeOffer(state.Product, state.Signals, seller, state.Verdict)
	item := storeOffer{product: product, offer: offer, root: b.roots[product.Card.Publication.CategoryID]}
	if product.Card.Publication.Published {
		b.candidates.add(item, b.paths)
	}
	if !offer.Visible {
		return nil
	}
	return b.addVisible(item)
}

func (b *storeSummaryBuilder) addVisible(item storeOffer) error {
	b.visible.add(item, b.paths)
	group := catalog.RatingGroup(item.product)
	if group != b.lastRatingGroup {
		b.weighted += item.product.AverageRating * float64(item.product.RatingsCount)
		b.ratings += item.product.RatingsCount
		b.lastRatingGroup = group
	}
	b.best = append(b.best, item)
	sortStoreOffers(b.best)
	b.best = b.best[:min(len(b.best), storeProductsSize)]
	// Порядок групп и товаров устойчив; потоковый хеш не хранит каталог и не включает цены/ранги.
	return json.NewEncoder(b.signature).Encode(storeProductSignature{
		Product:    storeProduct(item),
		CategoryID: item.product.Card.Publication.CategoryID,
	})
}

func (t *storeTotals) add(item storeOffer, paths map[string][]StoreCategory) {
	t.offers++
	price := item.offer.Price.Kopecks()
	if t.fromPrice == nil || price < *t.fromPrice {
		t.fromPrice = &price
	}
	for _, category := range paths[item.product.Card.Publication.CategoryID] {
		category.Count = t.categories[category.ID].Count + 1
		t.categories[category.ID] = category
	}
}

func (b *storeSummaryBuilder) summary(p *participant.Participant) (StoreSummary, error) {
	totals := b.visible
	if totals.offers == 0 {
		totals = b.candidates
	}
	result := StoreSummary{
		Offers:         totals.offers,
		VisibleOffers:  b.visible.offers,
		FromPrice:      totals.fromPrice,
		CategoryCounts: totals.categories,
		Categories:     []StoreCategory{},
		Products:       []ports.ShopCardProduct{},
		Hidden:         append([]participant.CardHidden{}, p.State().StoreCard.Hidden...),
		Uncategorized:  b.uncategorized,
		Rating:         catalog.ProductRating(b.weighted/float64(b.ratings), b.ratings),
	}
	for id, category := range totals.categories {
		if b.roots[id].ID == id {
			result.Categories = append(result.Categories, category)
		}
	}
	sortStoreCategories(result.Categories)
	for _, item := range b.best {
		result.Products = append(result.Products, storeProduct(item))
	}
	result.CacheSignature = fmt.Sprintf("%x", b.signature.Sum(nil))
	return result, nil
}

func sortStoreCategories(categories []StoreCategory) {
	sort.Slice(categories, func(i, j int) bool {
		a, b := categories[i], categories[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})
}
