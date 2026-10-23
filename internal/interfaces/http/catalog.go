package http

import (
	"net/http"
	"strconv"
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
	ID                string         `json:"id"`
	TenantID          string         `json:"tenantId"`
	ProductID         string         `json:"productId"`
	Seller            sellerJSON     `json:"seller"`
	Name              string         `json:"name"`
	Description       string         `json:"description,omitempty"`
	Image             string         `json:"image,omitempty"`
	URL               string         `json:"url,omitempty"`
	SKU               string         `json:"sku,omitempty"`
	Categories        []categoryJSON `json:"categories"`
	Tags              []tagJSON      `json:"tags"`
	Price             int64          `json:"price"`
	BasePrice         int64          `json:"basePrice"`
	Available         int            `json:"available"`
	InStock           bool           `json:"inStock"`
	CheckoutAvailable bool           `json:"checkoutAvailable"`
	UpdatedAt         time.Time      `json:"updatedAt"`
}

type offerPageJSON struct {
	Items  []offerJSON `json:"items"`
	Total  int         `json:"total"`
	Limit  int         `json:"limit"`
	Offset int         `json:"offset"`
}

func toOfferJSON(doc ports.OfferDocument, tags []tagJSON, withDescription bool) offerJSON {
	out := offerJSON{
		ID: doc.ID, TenantID: doc.TenantID, ProductID: doc.ProductID,
		Seller: sellerJSON{Slug: doc.SellerSlug, Name: doc.SellerName},
		Name:   doc.Name, Image: doc.Image, URL: doc.URL, SKU: doc.SKU,
		Categories: make([]categoryJSON, 0, len(doc.Categories)), Tags: tags,
		Price: doc.Price, BasePrice: doc.BasePrice, Available: doc.Available, InStock: doc.InStock,
		CheckoutAvailable: doc.CheckoutAvailable, UpdatedAt: doc.UpdatedAt,
	}
	if withDescription {
		out.Description = doc.Description
	}
	for _, c := range doc.Categories {
		out.Categories = append(out.Categories, categoryJSON{ID: c.ID, Name: c.Name, URL: c.URL})
	}

	return out
}

type catalogHandlers struct {
	queries *appcatalog.Queries
}

// list — выдача: GET /api/v1/catalog/offers?q=&tenant=&category=&tag=&priceMin=&priceMax=&inStock=&sort=&limit=&offset=
func (h *catalogHandlers) list(w http.ResponseWriter, r *http.Request) {
	query, err := parseOfferQuery(r)
	if err != nil {
		writeError(w, r, err)

		return
	}

	page, err := h.queries.Search(r.Context(), query)
	if err != nil {
		writeError(w, r, err)

		return
	}

	limit := query.Limit
	if limit == 0 {
		limit = appcatalog.DefaultLimit
	}

	body := offerPageJSON{Items: make([]offerJSON, 0, len(page.Items)), Total: page.Total, Limit: limit, Offset: query.Offset}
	for _, doc := range page.Items {
		tags := make([]tagJSON, 0, len(doc.Tags))
		for _, tag := range doc.Tags {
			tags = append(tags, tagJSON{ID: tag, Label: tag})
		}

		body.Items = append(body.Items, toOfferJSON(doc, tags, false))
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

	tags := make([]tagJSON, 0, len(card.Tags))
	for _, tag := range card.Tags {
		tags = append(tags, tagJSON{ID: tag.ID, Label: tag.Label})
	}

	writeJSON(w, http.StatusOK, toOfferJSON(card.Offer, tags, true))
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
