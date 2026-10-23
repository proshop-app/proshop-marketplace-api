package fake

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

// Index — индекс предложений в памяти с поколениями и версиями документов.
type Index struct {
	mu          sync.Mutex
	generations map[string]map[string]ports.OfferDocument
	active      string
	counter     int
	// FailPut, если задан, возвращается из записи документов.
	FailPut error
}

// NewIndex — индекс без рабочего поколения.
func NewIndex() *Index {
	return &Index{generations: map[string]map[string]ports.OfferDocument{}}
}

// Exists — рабочее поколение заведено.
func (x *Index) Exists(context.Context) (bool, error) {
	x.mu.Lock()
	defer x.mu.Unlock()

	return x.active != "", nil
}

// Put пишет в рабочее поколение, заводя его при первом обращении.
func (x *Index) Put(ctx context.Context, docs []ports.OfferDocument) error {
	x.mu.Lock()
	if x.active == "" {
		x.counter++
		x.active = fmt.Sprintf("gen-%d", x.counter)
		x.generations[x.active] = map[string]ports.OfferDocument{}
	}
	active := x.active
	x.mu.Unlock()

	return x.PutGeneration(ctx, active, docs)
}

// PutGeneration пишет документы; документ с версией ниже записанной пропускается.
func (x *Index) PutGeneration(_ context.Context, generation string, docs []ports.OfferDocument) error {
	x.mu.Lock()
	defer x.mu.Unlock()

	if x.FailPut != nil {
		return x.FailPut
	}

	target, ok := x.generations[generation]
	if !ok {
		return fmt.Errorf("нет поколения %s", generation)
	}

	for _, doc := range docs {
		if stored, ok := target[doc.ID]; ok && stored.Version > doc.Version {
			continue
		}

		target[doc.ID] = doc
	}

	return nil
}

// Get — документ рабочего поколения.
func (x *Index) Get(_ context.Context, id string) (ports.OfferDocument, bool, error) {
	x.mu.Lock()
	defer x.mu.Unlock()

	doc, ok := x.generations[x.active][id]

	return doc, ok, nil
}

// Search — выдача рабочего поколения: только предложения в выдаче.
func (x *Index) Search(_ context.Context, query ports.OfferQuery) (ports.OfferPage, error) {
	x.mu.Lock()
	defer x.mu.Unlock()

	var hits []ports.OfferDocument

	for _, doc := range x.generations[x.active] {
		if matches(doc, query) {
			hits = append(hits, doc)
		}
	}

	sort.Slice(hits, func(i, j int) bool {
		switch query.Sort {
		case ports.SortPriceAsc:
			if hits[i].Price != hits[j].Price {
				return hits[i].Price < hits[j].Price
			}
		case ports.SortPriceDesc:
			if hits[i].Price != hits[j].Price {
				return hits[i].Price > hits[j].Price
			}
		case ports.SortNewest:
			if !hits[i].UpdatedAt.Equal(hits[j].UpdatedAt) {
				return hits[i].UpdatedAt.After(hits[j].UpdatedAt)
			}
		}

		return hits[i].ID < hits[j].ID
	})

	page := ports.OfferPage{Facets: fakeFacets(hits, query.FacetFields), Total: len(hits)}
	for i := query.Offset; i < len(hits) && len(page.Items) < query.Limit; i++ {
		page.Items = append(page.Items, hits[i])
	}

	return page, nil
}

func matches(doc ports.OfferDocument, query ports.OfferQuery) bool {
	if !doc.Listed {
		return false
	}
	if query.Text != "" && !strings.Contains(strings.ToLower(doc.Name), strings.ToLower(query.Text)) {
		return false
	}
	if query.TenantID != "" && doc.TenantID != query.TenantID {
		return false
	}
	if (query.InStockSet || query.InStock) && doc.InStock != query.InStock {
		return false
	}
	if query.PriceMin != nil && doc.Price < *query.PriceMin {
		return false
	}
	if query.PriceMax != nil && doc.Price > *query.PriceMax {
		return false
	}
	if query.Tag != "" && !contains(doc.Tags, query.Tag) {
		return false
	}
	for code, filter := range query.Attributes {
		if !attributeMatches(doc.Attributes[code], filter) {
			return false
		}
	}
	if query.CategoryID != "" && !contains(doc.CategoryPath, query.CategoryID) {
		return false
	}

	return true
}

func contains(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}

	return false
}

// NewGeneration заводит пустое поколение.
func (x *Index) NewGeneration(context.Context) (string, error) {
	x.mu.Lock()
	defer x.mu.Unlock()

	x.counter++
	name := fmt.Sprintf("gen-%d", x.counter)
	x.generations[name] = map[string]ports.OfferDocument{}

	return name, nil
}

// Activate делает поколение рабочим и удаляет прежние.
func (x *Index) Activate(_ context.Context, generation string) error {
	x.mu.Lock()
	defer x.mu.Unlock()

	if _, ok := x.generations[generation]; !ok {
		return fmt.Errorf("нет поколения %s", generation)
	}

	for name := range x.generations {
		if name != generation {
			delete(x.generations, name)
		}
	}

	x.active = generation

	return nil
}

// Doc — документ рабочего поколения для проверок в тестах.
func (x *Index) Doc(id string) (ports.OfferDocument, bool) {
	doc, ok, _ := x.Get(context.Background(), id)

	return doc, ok
}

// Active — имя рабочего поколения.
func (x *Index) Active() string {
	x.mu.Lock()
	defer x.mu.Unlock()

	return x.active
}
