package http

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const shopDefaultLimit = 20

type shopHeaderJSON struct {
	ports.ShopHeader
	Subscribers int   `json:"subscribers"`
	Subscribed  *bool `json:"subscribed,omitempty"`
}

type hideCardJSON struct {
	Key    string `json:"key"`
	Reason string `json:"reason"`
}

func (h *participantHandlers) syncSettings(w http.ResponseWriter, r *http.Request) {
	id, err := ids.ParseTenantID(r.PathValue("tenantId"))
	if err == nil {
		err = h.service.SyncSettings(r.Context(), id)
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (h *participantHandlers) storeCard(w http.ResponseWriter, r *http.Request) {
	id, err := ids.ParseTenantID(r.PathValue("tenantId"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	result, err := h.service.StorePageSummary(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *participantHandlers) moderateCard(w http.ResponseWriter, r *http.Request) {
	id, err := ids.ParseTenantID(r.PathValue("tenantId"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	body := hideCardJSON{Key: r.PathValue("key")}
	hide := r.Method == http.MethodPost
	if hide {
		if err := readJSON(w, r, &body); err != nil {
			writeError(w, r, err)
			return
		}
	}
	result, err := h.service.ModerateCard(r.Context(), id, body.Key, hide, participant.Decision{
		Operator: principalFrom(r),
		Note:     body.Reason,
	})
	respond(w, r, http.StatusOK, result, err)
}

func (h *participantHandlers) shop(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Vary", "Cookie, Authorization")
	header, err := h.service.Shop(r.Context(), r.PathValue("slug"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	result := shopHeaderJSON{ShopHeader: header}
	id := BuyerID(r.Context())
	result.Subscribers, result.Subscribed, err = h.buyerFeatures.Subscribers(r.Context(), header.TenantID, id)
	if err != nil {
		writeError(w, r, err)
		return
	}

	if id != "" {
		w.Header().Set("Cache-Control", "private, no-store")
		writeJSON(w, http.StatusOK, result)
		return
	}
	writeShopJSON(w, r, result)
}

func (h *participantHandlers) shops(w http.ResponseWriter, r *http.Request) {
	limit, offset := shopDefaultLimit, 0
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
	result, err := h.service.Shops(r.Context(), r.URL.Query().Get("category"), limit, offset)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeShopJSON(w, r, result)
}

// writeShopJSON разрешает публичный кэш с проверкой актуальности готовой сводки.
func writeShopJSON(w http.ResponseWriter, r *http.Request, result any) {
	raw, err := json.Marshal(result)
	if err != nil {
		writeError(w, r, err)
		return
	}
	etag := fmt.Sprintf(`"%x"`, sha256.Sum256(raw))
	if publicCached(w, r, etag) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(append(raw, '\n'))
}
