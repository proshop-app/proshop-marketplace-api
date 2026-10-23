package http

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type sellerJSON struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type categoryJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
}

type tagJSON struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// offerJSON — предложение в ответе. Суммы — целые копейки.
type offerJSON struct {
	CategoryID        string                     `json:"categoryId"`
	Attributes        map[string]json.RawMessage `json:"attributes"`
	ID                string                     `json:"id"`
	TenantID          string                     `json:"tenantId"`
	ProductID         string                     `json:"productId"`
	Seller            sellerJSON                 `json:"seller"`
	Name              string                     `json:"name"`
	Description       string                     `json:"description,omitempty"`
	Image             string                     `json:"image,omitempty"`
	URL               string                     `json:"url,omitempty"`
	SKU               string                     `json:"sku,omitempty"`
	Categories        []categoryJSON             `json:"categories"`
	Tags              []tagJSON                  `json:"tags"`
	Price             int64                      `json:"price"`
	BasePrice         int64                      `json:"basePrice"`
	Available         int                        `json:"available"`
	InStock           bool                       `json:"inStock"`
	CheckoutAvailable bool                       `json:"checkoutAvailable"`
	UpdatedAt         time.Time                  `json:"updatedAt"`
}

type offerPageJSON struct {
	Facets []ports.Facet `json:"facets"`
	Items  []offerJSON   `json:"items"`
	Total  int           `json:"total"`
	Limit  int           `json:"limit"`
	Offset int           `json:"offset"`
}

type catalogHandlers struct {
	queries *appcatalog.Queries
}

func toOfferJSON(card appcatalog.OfferCard, withDescription bool) offerJSON {
	doc := card.Offer
	tags := make([]tagJSON, 0, len(card.Tags))
	for _, tag := range card.Tags {
		tags = append(tags, tagJSON{ID: tag.ID, Label: tag.Label})
	}

	out := offerJSON{
		CategoryID:        doc.CategoryID,
		Attributes:        doc.Attributes,
		ID:                doc.ID,
		TenantID:          doc.TenantID,
		ProductID:         doc.ProductID,
		Seller:            sellerJSON{Slug: doc.SellerSlug, Name: doc.SellerName},
		Name:              doc.Name,
		Image:             doc.Image,
		URL:               doc.URL,
		SKU:               doc.SKU,
		Categories:        make([]categoryJSON, 0, len(doc.Categories)),
		Tags:              tags,
		Price:             doc.Price,
		BasePrice:         doc.BasePrice,
		Available:         doc.Available,
		InStock:           doc.InStock,
		CheckoutAvailable: doc.CheckoutAvailable,
		UpdatedAt:         doc.UpdatedAt.UTC(),
	}
	if out.Attributes == nil {
		out.Attributes = map[string]json.RawMessage{}
	}
	if withDescription {
		out.Description = doc.Description
	}
	for _, c := range doc.Categories {
		out.Categories = append(out.Categories, categoryJSON{ID: c.ID, Name: c.Name, URL: c.URL})
	}

	return out
}

// list — выдача: GET /api/v1/catalog/offers?q=&tenant=&category=&tag=&priceMin=&priceMax=&inStock=&sort=&limit=&offset=
func (h *catalogHandlers) list(w http.ResponseWriter, r *http.Request) {
	query, err := parseOfferQuery(r)
	if err != nil {
		writeError(w, r, err)

		return
	}

	list, err := h.queries.Search(r.Context(), query)
	if err != nil {
		writeError(w, r, err)

		return
	}

	limit := query.Limit
	if limit == 0 {
		limit = appcatalog.DefaultLimit
	}

	body := offerPageJSON{
		Facets: list.Facets,
		Items:  make([]offerJSON, 0, len(list.Items)),
		Total:  list.Total,
		Limit:  limit,
		Offset: query.Offset,
	}
	for _, card := range list.Items {
		body.Items = append(body.Items, toOfferJSON(card, false))
	}

	writeJSON(w, http.StatusOK, body)
}

// card — карточка: GET /api/v1/catalog/offers/{offerId}
func (h *catalogHandlers) card(w http.ResponseWriter, r *http.Request) {
	offerID, err := ids.ParseOfferID(r.PathValue("offerId"))
	if err != nil {
		writeError(w, r, err)

		return
	}

	card, err := h.queries.Offer(r.Context(), offerID)
	if err != nil {
		writeError(w, r, err)

		return
	}

	writeJSON(w, http.StatusOK, toOfferJSON(card, true))
}

func parseOfferQuery(r *http.Request) (ports.OfferQuery, error) {
	values := r.URL.Query()
	query := ports.OfferQuery{
		Text:       values.Get("q"),
		TenantID:   values.Get("tenant"),
		CategoryID: values.Get("category"),
		Tag:        values.Get("tag"),
		Sort:       ports.OfferSort(values.Get("sort")),
	}

	query.Attributes = map[string]ports.AttributeFilter{}
	for key, values := range values {
		if !strings.HasPrefix(key, "attr.") {
			continue
		}
		code := strings.TrimPrefix(key, "attr.")
		var bound string
		if strings.HasSuffix(code, ".min") {
			code = strings.TrimSuffix(code, ".min")
			bound = "min"
		} else if strings.HasSuffix(code, ".max") {
			code = strings.TrimSuffix(code, ".max")
			bound = "max"
		}
		filter := query.Attributes[code]
		if bound != "" {
			if len(values) != 1 || values[0] == "" {
				return query, appcatalog.ErrInvalidQuery
			}
			if bound == "min" {
				filter.Min = values[0]
			} else {
				filter.Max = values[0]
			}
		} else {
			filter.Values = values
		}
		query.Attributes[code] = filter
	}
	var err error

	if query.Limit, err = intParam(values.Get("limit")); err != nil {
		return query, appcatalog.ErrInvalidQuery.WithMessage("limit — целое число")
	}
	if query.Offset, err = intParam(values.Get("offset")); err != nil {
		return query, appcatalog.ErrInvalidQuery.WithMessage("offset — целое число")
	}
	if query.PriceMin, err = kopecksParam(values.Get("priceMin")); err != nil {
		return query, appcatalog.ErrInvalidQuery.WithMessage("priceMin — целое число копеек")
	}
	if query.PriceMax, err = kopecksParam(values.Get("priceMax")); err != nil {
		return query, appcatalog.ErrInvalidQuery.WithMessage("priceMax — целое число копеек")
	}

	if raw := values.Get("inStock"); raw != "" {
		query.InStockSet = true
		if query.InStock, err = strconv.ParseBool(raw); err != nil {
			return query, appcatalog.ErrInvalidQuery.WithMessage("inStock — true или false")
		}
	}

	return query, nil
}

func intParam(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}

	return strconv.Atoi(raw)
}

func kopecksParam(raw string) (*int64, error) {
	if raw == "" {
		return nil, nil
	}

	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil, err
	}

	return &value, nil
}
