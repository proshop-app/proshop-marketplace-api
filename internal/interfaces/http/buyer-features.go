package http

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"

	appbuyer "github.com/proshop-app/proshop-marketplace/internal/application/buyer"
	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

type buyerFeatureHandlers struct {
	service        *appbuyer.Features
	trustedProxies int
}

type alertsJSON struct {
	PriceDrops *int `json:"priceDrops"`
}

type profileJSON struct {
	Name          string              `json:"name"`
	Region        *buyer.Region       `json:"region"`
	PickupPoint   *buyer.Pickup       `json:"pickupPoint"`
	Notifications buyer.Notifications `json:"notifications"`
	Alerts        alertsJSON          `json:"alerts"`
}

type favoriteJSON struct {
	offerJSON
	Listed bool `json:"listed"`
}

type watchJSON struct {
	Offer        offerJSON `json:"offer"`
	PriceAtWatch rubles    `json:"priceAtWatch"`
	CurrentPrice *rubles   `json:"currentPrice"`
	TargetPrice  *rubles   `json:"targetPrice,omitempty"`
	Dropped      bool      `json:"dropped"`
}

type targetPriceJSON struct {
	TargetPrice *rubles `json:"targetPrice"`
}

func privateBuyer(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		next(w, r)
	}
}

func (h *buyerFeatureHandlers) profile(w http.ResponseWriter, r *http.Request) {
	id := BuyerID(r.Context())
	result, err := h.service.Profile(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, profileResponse(result))
}

func (h *buyerFeatureHandlers) patchProfile(w http.ResponseWriter, r *http.Request) {
	id := BuyerID(r.Context())
	var patch map[string]json.RawMessage
	if err := readJSON(w, r, &patch); err != nil {
		writeError(w, r, err)
		return
	}
	if patch == nil {
		writeError(w, r, buyer.ErrInvalidProfile)
		return
	}
	result, err := h.service.Patch(r.Context(), id, func(state buyer.ProfileState) (buyer.ProfileState, error) {
		for key, raw := range patch {
			var target any
			switch key {
			case "name":
				target = &state.Name
			case "region":
				// Разбор в старый указатель дописал бы прежние поля: ПВЗ с тем же кодом
				// совпал бы с сохранённым и прошёл без проверки справочника.
				state.Region = nil
				target = &state.Region
			case "pickupPoint":
				state.PickupPoint = nil
				target = &state.PickupPoint
			case "notifications":
				target = &state.Notifications
			default:
				return state, buyer.ErrInvalidProfile
			}
			if (key == "name" || key == "notifications") && string(raw) == "null" {
				return state, buyer.ErrInvalidProfile
			}
			decoder := json.NewDecoder(strings.NewReader(string(raw)))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(target); err != nil {
				return state, buyer.ErrInvalidProfile
			}
		}
		return state, nil
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, profileResponse(result))
}

func (h *buyerFeatureHandlers) region(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.Region(r.Context(), BuyerID(r.Context()), clientIP(r, h.trustedProxies))
	if err != nil {
		var limited *appbuyer.RegionLimitError
		if errors.As(err, &limited) {
			seconds := max(1, int(math.Ceil(limited.RetryAfter.Seconds())))
			w.Header().Set("Retry-After", strconv.Itoa(seconds))
		}
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *buyerFeatureHandlers) favorites(w http.ResponseWriter, r *http.Request) {
	limit, offset := 20, 0
	for key, target := range map[string]*int{"limit": &limit, "offset": &offset} {
		if raw := r.URL.Query().Get(key); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 0 {
				writeError(w, r, ErrInvalidQuery)
				return
			}
			*target = value
		}
	}
	if limit < 1 || limit > buyer.FavoritesLimit {
		writeError(w, r, ErrInvalidQuery)
		return
	}
	items, total, err := h.service.Favorites(r.Context(), BuyerID(r.Context()), limit, offset)
	if err != nil {
		writeError(w, r, err)
		return
	}
	result := struct {
		Items []favoriteJSON `json:"items"`
		Total int            `json:"total"`
	}{Items: []favoriteJSON{}, Total: total}
	for _, item := range items {
		result.Items = append(result.Items, favoriteJSON{
			offerJSON: toOfferJSON(appcatalog.OfferCard{Offer: item.Offer}, false),
			Listed:    item.Listed,
		})
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *buyerFeatureHandlers) watches(w http.ResponseWriter, r *http.Request) {
	items, err := h.service.Saved(r.Context(), BuyerID(r.Context()))
	if err != nil {
		writeError(w, r, err)
		return
	}
	result := struct {
		Items []watchJSON `json:"items"`
	}{Items: []watchJSON{}}
	for _, item := range items {
		result.Items = append(result.Items, watchJSON{
			Offer:        toOfferJSON(appcatalog.OfferCard{Offer: item.Offer}, false),
			PriceAtWatch: rubles(item.PriceAtWatch),
			CurrentPrice: rublesPointer(item.CurrentPrice),
			TargetPrice:  rublesPointer(item.TargetPrice),
			Dropped:      item.Dropped,
		})
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *buyerFeatureHandlers) putSaved(kind ports.SavedKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		offer, err := ids.ParseOfferID(r.PathValue("offerId"))
		if err != nil {
			writeError(w, r, err)
			return
		}

		var target *money.Money
		if kind == ports.PriceWatches {
			var body targetPriceJSON
			if err := readJSON(w, r, &body); err != nil {
				writeError(w, r, err)
				return
			}
			if body.TargetPrice != nil {
				v := money.FromKopecks(int64(*body.TargetPrice))
				target = &v
			}
		}
		err = h.service.PutSaved(r.Context(), BuyerID(r.Context()), kind, offer, target)
		if err != nil {
			writeError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *buyerFeatureHandlers) deleteSaved(kind ports.SavedKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		offer, err := ids.ParseOfferID(r.PathValue("offerId"))
		if err == nil {
			err = h.service.DeleteSaved(r.Context(), BuyerID(r.Context()), kind, offer.String())
		}
		if err != nil {
			writeError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *buyerFeatureHandlers) subscriptions(w http.ResponseWriter, r *http.Request) {
	items, err := h.service.Subscriptions(r.Context(), BuyerID(r.Context()))
	if err != nil {
		writeError(w, r, err)
		return
	}
	result := struct {
		Items []shopCardJSON `json:"items"`
	}{Items: shopCardResponses(items)}
	writeJSON(w, http.StatusOK, result)
}

func (h *buyerFeatureHandlers) putSubscription(w http.ResponseWriter, r *http.Request) {
	h.subscription(w, r, ports.Subscribed)
}

func (h *buyerFeatureHandlers) deleteSubscription(w http.ResponseWriter, r *http.Request) {
	h.subscription(w, r, ports.Unsubscribed)
}

func (h *buyerFeatureHandlers) subscription(w http.ResponseWriter, r *http.Request, state ports.SubscriptionState) {
	tenant, err := ids.ParseTenantID(r.PathValue("tenantId"))
	if err == nil {
		err = h.service.Subscribe(r.Context(), BuyerID(r.Context()), tenant, state)
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func profileResponse(view appbuyer.ProfileView) profileJSON {
	return profileJSON{
		Name:          view.Name,
		Region:        view.Region,
		PickupPoint:   view.PickupPoint,
		Notifications: view.Notifications,
		Alerts:        alertsJSON{PriceDrops: view.Alerts.PriceDrops},
	}
}
