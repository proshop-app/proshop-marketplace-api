package http

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type sellerJSON struct {
	Delivery participant.DeliverySettings `json:"delivery"`
	Slug     string                       `json:"slug"`
	Name     string                       `json:"name"`
}

type categoryJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
}

// offerJSON — предложение в ответе. Суммы — числовые рубли.
type offerJSON struct {
	SellersCount      *int                                   `json:"sellersCount,omitempty"`
	Assets            *[]string                              `json:"assets,omitempty"`
	Rating            *catalog.Rating                        `json:"rating"`
	Variants          *variantGroupJSON                      `json:"variants,omitempty"`
	DeliveryAvailable *bool                                  `json:"deliveryAvailable,omitempty"`
	CategoryID        string                                 `json:"categoryId"`
	AttributeDetails  map[string]appcatalog.AttributeDisplay `json:"attributeDetails,omitempty"`
	Attributes        map[string]json.RawMessage             `json:"attributes"`
	ID                string                                 `json:"id"`
	TenantID          string                                 `json:"tenantId"`
	ProductID         string                                 `json:"productId"`
	Seller            sellerJSON                             `json:"seller"`
	Name              string                                 `json:"name"`
	Description       string                                 `json:"description,omitempty"`
	Image             string                                 `json:"image,omitempty"`
	URL               string                                 `json:"url,omitempty"`
	SKU               string                                 `json:"sku,omitempty"`
	Categories        []categoryJSON                         `json:"categories"`
	Price             rubles                                 `json:"price"`
	BasePrice         rubles                                 `json:"basePrice"`
	Available         int                                    `json:"available"`
	InStock           bool                                   `json:"inStock"`
	CheckoutAvailable bool                                   `json:"checkoutAvailable"`
	UpdatedAt         time.Time                              `json:"updatedAt"`
}

type offerPageJSON struct {
	FacetCategoryID    string               `json:"facetCategoryId,omitempty"`
	AvailableFacetKeys []string             `json:"availableFacetKeys,omitempty"`
	Correction         *offerCorrectionJSON `json:"correction,omitempty"`
	Receipt            string               `json:"receipt,omitempty"`
	Facets             []facetJSON          `json:"facets"`
	Items              []offerJSON          `json:"items"`
	Total              int                  `json:"total"`
	TotalRelation      string               `json:"totalRelation"`
	Limited            bool                 `json:"limited"`
	Limit              int                  `json:"limit"`
	Offset             int                  `json:"offset"`
}

type offerCorrectionJSON struct {
	Query string `json:"query"`
	Kind  string `json:"kind"`
}

type catalogHandlers struct {
	sellers ports.SellersCounts
	queries *appcatalog.Queries
	search  *searchHandlers
}

func toOfferJSON(card appcatalog.OfferCard, withDescription bool) offerJSON {
	doc := card.Offer
	out := offerJSON{
		SellersCount: card.SellersCount,
		Rating:       doc.Rating,
		CategoryID:   doc.CategoryID,
		Attributes:   doc.Attributes,
		ID:           doc.ID,
		TenantID:     doc.TenantID,
		ProductID:    doc.ProductID,
		Seller: sellerJSON{
			Slug:     doc.SellerSlug,
			Name:     doc.SellerName,
			Delivery: doc.Delivery.Clone(),
		},
		Name:              doc.Name,
		Image:             doc.Image,
		URL:               doc.URL,
		SKU:               doc.SKU,
		Categories:        make([]categoryJSON, 0, len(doc.Categories)),
		Price:             rubles(doc.Price),
		BasePrice:         rubles(doc.BasePrice),
		Available:         doc.Available,
		InStock:           doc.InStock,
		CheckoutAvailable: doc.CheckoutAvailable,
		UpdatedAt:         doc.UpdatedAt.UTC(),
	}
	if out.Attributes == nil {
		out.Attributes = map[string]json.RawMessage{}
	}
	if withDescription {
		assets := append([]string{}, doc.Assets...)
		out.Assets = &assets
		out.Description = doc.Description
		out.AttributeDetails = card.AttributeDetails
		if doc.Variants != nil {
			out.Variants = &variantGroupJSON{GroupID: doc.Variants.GroupID}
		}
	}
	for _, c := range doc.Categories {
		out.Categories = append(out.Categories, categoryJSON{ID: c.ID, Name: c.Name, URL: c.URL})
	}

	return out
}

// list — выдача: GET /api/v1/catalog/offers?q=&tenant=&category=&priceMin=&priceMax=&inStock=&sort=&limit=&offset=
func (h *catalogHandlers) list(w http.ResponseWriter, r *http.Request) {
	values := r.URL.Query()
	observe, requested := values["observe"]
	if requested {
		if len(observe) != 1 || observe[0] != "1" {
			writeError(w, r, appcatalog.ErrInvalidQuery.WithMessage("observe — 1"))
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
	}
	if values.Has("ids") {
		h.listIDs(w, r)
		return
	}

	query, err := parseCatalogOfferQuery(r)
	if err != nil {
		writeError(w, r, err)

		return
	}
	query.Observe = requested && query.Text != "" && h.search.feedback != nil
	if query.Observe {
		query.BuyerKey = h.search.buyerKey(r)
	}

	list, err := h.queries.Search(r.Context(), query)
	if err != nil {
		writeError(w, r, err)

		return
	}
	if !h.populateSellers(r, list.Items) {
		return
	}
	rememberCatalogFacets(r.Context(), query, list.Facets)
	facets, err := facetResponses(list.Facets)
	if err != nil {
		writeError(w, r, err)
		return
	}
	body := offerPageJSON{
		AvailableFacetKeys: list.AvailableFacetKeys,
		FacetCategoryID:    list.FacetCategoryID,
		Receipt:            list.Receipt,
		Facets:             facets,
		Items:              make([]offerJSON, 0, len(list.Items)),
		Total:              list.Total,
		TotalRelation:      list.TotalRelation,
		Limited:            list.Limited,
		Limit:              list.Limit,
		Offset:             query.Offset,
	}
	if list.Correction != nil {
		body.Correction = &offerCorrectionJSON{
			Query: list.Correction.Query,
			Kind:  list.Correction.Kind,
		}
	}
	for _, card := range list.Items {
		item := toOfferJSON(card, false)
		annotateDelivery(&item, r)
		body.Items = append(body.Items, item)
	}

	writeOfferPage(w, body)
}

func writeOfferPage(w http.ResponseWriter, body offerPageJSON) {
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", publicCatalogCacheControl)
	}
	writeJSON(w, http.StatusOK, body)
}

// card — карточка: GET /api/v1/catalog/offers/{offerId}
func (h *catalogHandlers) card(w http.ResponseWriter, r *http.Request) {
	if err := validateDeliveryContext(r); err != nil {
		writeError(w, r, err)
		return
	}
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
	cards := []appcatalog.OfferCard{card}
	if !h.populateSellers(r, cards) {
		return
	}
	body := toOfferJSON(cards[0], true)
	annotateDelivery(&body, r)
	w.Header().Set("Cache-Control", publicCatalogCacheControl)
	writeJSON(w, http.StatusOK, body)
}

// parseCatalogOfferQuery добавляет exact только каталогу: общий разбор используется также рекламой.
func parseCatalogOfferQuery(r *http.Request) (ports.OfferQuery, error) {
	query, err := parseOfferQuery(r)
	if err != nil {
		return query, err
	}

	if exact, present := r.URL.Query()["exact"]; present {
		if len(exact) != 1 || (exact[0] != "true" && exact[0] != "false") {
			return query, appcatalog.ErrInvalidQuery.WithMessage("exact — true или false")
		}
		query.Exact = exact[0] == "true"
	}

	query.FacetKeys = r.URL.Query()["facetKeys"]
	if histogram, present := r.URL.Query()["priceHistogram"]; present {
		if len(histogram) != 1 || (histogram[0] != "true" && histogram[0] != "false") {
			return query, appcatalog.ErrInvalidQuery.WithMessage("priceHistogram — true или false")
		}
		query.PriceHistogram = histogram[0] == "true"
	}

	return query, nil
}

func parseOfferQuery(r *http.Request) (ports.OfferQuery, error) {
	text, err := appcatalog.NormalizeQueryText(r.URL.Query().Get("q"))
	if err != nil {
		return ports.OfferQuery{}, err
	}
	if err := validateDeliveryContext(r); err != nil {
		return ports.OfferQuery{}, err
	}
	values := r.URL.Query()
	query := ports.OfferQuery{
		Text:       text,
		TenantIDs:  values["tenant"],
		GroupID:    values.Get("groupId"),
		CategoryID: values.Get("category"),
		Sort:       ports.OfferSort(values.Get("sort")),
	}
	for _, key := range []string{"category", "categoryId"} {
		if len(values[key]) > 1 {
			return query, appcatalog.ErrInvalidQuery.WithMessage("категория должна быть указана один раз")
		}
	}
	if alias, present := values["categoryId"]; present {
		if _, canonical := values["category"]; canonical && query.CategoryID != alias[0] {
			return query, appcatalog.ErrInvalidQuery.WithMessage("category и categoryId противоречат друг другу")
		}
		query.CategoryID = alias[0]
	}
	if err := parseOfferFacetSelection(values, &query); err != nil {
		return query, err
	}

	if tracks, present := values["track"]; present {
		if len(tracks) != 1 || (tracks[0] != "true" && tracks[0] != "1") {
			return query, appcatalog.ErrInvalidQuery.WithMessage("track — true или 1")
		}
		query.TrackQuery = true
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
	if query.Limit, err = intParam(values.Get("limit")); err != nil {
		return query, appcatalog.ErrInvalidQuery.WithMessage("limit — целое число")
	}
	if query.Offset, err = intParam(values.Get("offset")); err != nil {
		return query, appcatalog.ErrInvalidQuery.WithMessage("offset — целое число")
	}
	if query.PriceMin, err = rublesParam(values.Get("priceMin")); err != nil {
		return query, appcatalog.ErrInvalidQuery.WithMessage("priceMin — рубли, не больше двух знаков после точки")
	}
	if query.PriceMax, err = rublesParam(values.Get("priceMax")); err != nil {
		return query, appcatalog.ErrInvalidQuery.WithMessage("priceMax — рубли, не больше двух знаков после точки")
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

func rublesParam(raw string) (*int64, error) {
	if raw == "" {
		return nil, nil
	}

	amount, err := parseRubles(raw)
	if err != nil {
		return nil, err
	}

	value := amount.Kopecks()
	return &value, nil
}

// annotateDelivery оставляет предложение видимым, но явно запрещает доставку вне зон продавца.
func annotateDelivery(item *offerJSON, r *http.Request) {
	carrier, region := r.URL.Query().Get("deliveryCarrier"), r.URL.Query().Get("deliveryRegion")
	if carrier == "" {
		return
	}
	allowed := item.Seller.Delivery.Allows(carrier, region)
	item.DeliveryAvailable = &allowed
}

func validateDeliveryContext(r *http.Request) error {
	carrier, region := r.URL.Query().Get("deliveryCarrier"), r.URL.Query().Get("deliveryRegion")
	if (carrier == "") != (region == "") || len(carrier) > 64 || len(region) > 128 {
		return ErrInvalidQuery.WithMessage("deliveryCarrier и deliveryRegion передаются вместе")
	}
	return nil
}
