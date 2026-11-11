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
	Delivery         participant.DeliverySettings `json:"delivery"`
	FreeDeliveryFrom rubles                       `json:"freeDeliveryFrom"`
	Slug             string                       `json:"slug"`
	Name             string                       `json:"name"`
}

type categoryJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
}

// offerJSON — предложение в ответе. Суммы — числовые рубли.
type offerJSON struct {
	Assets            *[]string                  `json:"assets,omitempty"`
	Rating            *catalog.Rating            `json:"rating"`
	Variants          *variantBlockJSON          `json:"variants,omitempty"`
	DeliveryAvailable *bool                      `json:"deliveryAvailable,omitempty"`
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
	Price             rubles                     `json:"price"`
	BasePrice         rubles                     `json:"basePrice"`
	Available         int                        `json:"available"`
	InStock           bool                       `json:"inStock"`
	CheckoutAvailable bool                       `json:"checkoutAvailable"`
	UpdatedAt         time.Time                  `json:"updatedAt"`
}

type offerPageJSON struct {
	Correction *offerCorrectionJSON `json:"correction,omitempty"`
	Facets     []facetJSON          `json:"facets"`
	Items      []offerJSON          `json:"items"`
	Total      int                  `json:"total"`
	Limit      int                  `json:"limit"`
	Offset     int                  `json:"offset"`
}

type offerCorrectionJSON struct {
	Query string `json:"query"`
	Kind  string `json:"kind"`
}

type catalogHandlers struct {
	queries *appcatalog.Queries
}

func toOfferJSON(card appcatalog.OfferCard, withDescription bool) offerJSON {
	doc := card.Offer
	out := offerJSON{
		Rating:     doc.Rating,
		CategoryID: doc.CategoryID,
		Attributes: doc.Attributes,
		ID:         doc.ID,
		TenantID:   doc.TenantID,
		ProductID:  doc.ProductID,
		Seller: sellerJSON{
			Slug:             doc.SellerSlug,
			Name:             doc.SellerName,
			Delivery:         doc.Delivery.Clone(),
			FreeDeliveryFrom: rubles(doc.FreeDeliveryFrom),
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
		out.Variants = variantResponse(card.Variants)
	}
	for _, c := range doc.Categories {
		out.Categories = append(out.Categories, categoryJSON{ID: c.ID, Name: c.Name, URL: c.URL})
	}

	return out
}

// list — выдача: GET /api/v1/catalog/offers?q=&tenant=&category=&priceMin=&priceMax=&inStock=&sort=&limit=&offset=
func (h *catalogHandlers) list(w http.ResponseWriter, r *http.Request) {
	query, err := parseOfferQuery(r)
	if err != nil {
		writeError(w, r, err)

		return
	}

	query.BuyerID = BuyerID(r.Context())
	list, err := h.queries.Search(r.Context(), query)
	if err != nil {
		writeError(w, r, err)

		return
	}
	rememberCatalogFacets(r.Context(), query, list.Facets)
	limit := query.Limit
	if limit == 0 {
		limit = appcatalog.DefaultLimit
	}
	facets, err := facetResponses(list.Facets)
	if err != nil {
		writeError(w, r, err)
		return
	}
	body := offerPageJSON{
		Facets: facets,
		Items:  make([]offerJSON, 0, len(list.Items)),
		Total:  list.Total,
		Limit:  limit,
		Offset: query.Offset,
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

	card, err := h.queries.Offer(r.Context(), offerID, BuyerID(r.Context()))
	if err != nil {
		writeError(w, r, err)

		return
	}
	body := toOfferJSON(card, true)
	annotateDelivery(&body, r)
	writeJSON(w, http.StatusOK, body)
}

func parseOfferQuery(r *http.Request) (ports.OfferQuery, error) {
	if err := validateDeliveryContext(r); err != nil {
		return ports.OfferQuery{}, err
	}
	values := r.URL.Query()
	query := ports.OfferQuery{
		Text:       values.Get("q"),
		TenantIDs:  values["tenant"],
		GroupID:    values.Get("groupId"),
		CategoryID: values.Get("category"),
		Sort:       ports.OfferSort(values.Get("sort")),
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
	var err error

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
