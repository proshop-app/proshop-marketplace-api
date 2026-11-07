package http

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	appoperator "github.com/proshop-app/proshop-marketplace/internal/application/operator"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/event"
	"github.com/proshop-app/proshop-marketplace/internal/domain/operator"
)

const (
	operatorCookie               = "__Host-kvazar_operator"
	operatorKeyCookie            = "__Host-kvazar_operator_key"
	keyOperator       contextKey = 100
)

type operatorHandlers struct {
	auth           *appoperator.Auth
	events         *messaging.Events
	origin         string
	trustedProxies int
}

type operatorProfileJSON struct {
	operator.Principal
	Exp int64 `json:"exp"`
}

type loginOperatorJSON struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type eventNoteJSON struct {
	Note            string `json:"note"`
	ExpectedVersion int64  `json:"expectedVersion"`
}

type parkedEventJSON struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	TenantID   string         `json:"tenantId"`
	OccurredAt time.Time      `json:"occurredAt"`
	Attempts   int            `json:"attempts"`
	Error      string         `json:"error"`
	Payload    map[string]any `json:"payload"`
	Status     string         `json:"status"`
	Resolution string         `json:"resolution"`
	OperatorID string         `json:"operatorId"`
	UpdatedAt  time.Time      `json:"updatedAt"`
	Version    int64          `json:"version"`
}

type eventListJSON struct {
	Items  []parkedEventJSON `json:"items"`
	Total  int               `json:"total"`
	Limit  int               `json:"limit"`
	Offset int               `json:"offset"`
}

func operatorToken(r *http.Request) string {
	cookie, err := r.Cookie(operatorCookie)
	if err != nil {
		return ""
	}
	return cookie.Value
}

func (h *operatorHandlers) csrf(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Origin") != h.origin || h.origin == "" ||
		r.Header.Get("X-Requested-With") != "KvazarOperator" {
		writeError(w, r, operator.ErrForbidden)
		return false
	}
	return true
}

func (h *operatorHandlers) only(machine string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var principal operator.Principal
		if r.Header.Get("Authorization") != "" {
			presented, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !found || machine == "" || subtle.ConstantTimeCompare([]byte(presented), []byte(machine)) != 1 {
				writeError(w, r, ErrUnauthorized)
				return
			}
			principal = operator.Principal{ID: "machine", Name: "Служебный доступ", Role: operator.Role}
		} else {
			if operatorToken(r) == "" {
				writeError(w, r, ErrUnauthorized)
				return
			}
			var err error
			principal, err = h.auth.Current(r.Context(), operatorToken(r))
			if err != nil {
				writeError(w, r, err)
				return
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead && !h.csrf(w, r) {
				return
			}
		}
		if err := operator.Require(principal); err != nil {
			writeError(w, r, err)
			return
		}
		ctx := context.WithValue(r.Context(), keyOperator, principal)
		next(w, r.WithContext(ctx))
	}
}

func (h *operatorHandlers) session(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !h.profileAllowed(w, r) {
		return
	}

	principal, access, err := h.auth.Profile(r.Context(), operatorToken(r), operatorKey(r))
	if err != nil {
		writeError(w, r, err)

		return
	}

	if access.Key != "" {
		setOperatorCookies(w, access)
	}

	writeJSON(w, http.StatusOK, operatorProfileJSON{
		Principal: principal,
		Exp:       access.AccessExpiresAt.Unix(),
	})
}

func (h *operatorHandlers) refresh(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !h.csrf(w, r) {
		return
	}

	principal, access, err := h.auth.Refresh(r.Context(), operatorKey(r))
	if err != nil {
		writeError(w, r, err)

		return
	}

	setOperatorCookies(w, access)
	writeJSON(w, http.StatusOK, operatorProfileJSON{
		Principal: principal,
		Exp:       access.AccessExpiresAt.Unix(),
	})
}

func (h *operatorHandlers) login(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !h.csrf(w, r) {
		return
	}
	if h.auth == nil {
		writeError(w, r, operator.ErrUnauthorized)
		return
	}
	var body loginOperatorJSON
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	principal, access, err := h.auth.Login(
		r.Context(),
		body.Email,
		body.Password,
		clientIP(r, h.trustedProxies),
		operatorKey(r),
	)
	if err != nil {
		writeError(w, r, err)
		return
	}
	setOperatorCookies(w, access)
	writeJSON(w, http.StatusOK, operatorProfileJSON{
		Principal: principal,
		Exp:       access.AccessExpiresAt.Unix(),
	})
}

func (h *operatorHandlers) logout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !h.csrf(w, r) {
		return
	}
	err := h.auth.Logout(r.Context(), operatorKey(r))
	clearOperatorCookies(w)
	if err != nil {
		writeError(w, r, err)

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func principalFrom(r *http.Request) operator.Principal {
	principal, _ := r.Context().Value(keyOperator).(operator.Principal)
	return principal
}

func (h *operatorHandlers) listEvents(w http.ResponseWriter, r *http.Request) {
	f := ports.EventFilter{
		TenantID: r.URL.Query().Get("tenantId"),
		Status:   r.URL.Query().Get("status"),
		Topic:    r.URL.Query().Get("type"),
		Reason:   r.URL.Query().Get("reason"),
	}
	for name, target := range map[string]*int{"limit": &f.Limit, "offset": &f.Offset} {
		if raw := r.URL.Query().Get(name); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil {
				writeError(w, r, ErrInvalidQuery)
				return
			}
			*target = value
		}
	}
	page, err := h.events.List(r.Context(), principalFrom(r), f)
	if err != nil {
		writeError(w, r, err)
		return
	}
	body := eventListJSON{Items: []parkedEventJSON{}, Total: page.Total, Limit: page.Limit, Offset: page.Offset}
	for _, item := range page.Items {
		body.Items = append(body.Items, eventJSON(item))
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, body)
}

func (h *operatorHandlers) retry(w http.ResponseWriter, r *http.Request)   { h.decide(w, r, true) }
func (h *operatorHandlers) resolve(w http.ResponseWriter, r *http.Request) { h.decide(w, r, false) }

func (h *operatorHandlers) decide(w http.ResponseWriter, r *http.Request, retry bool) {
	id, err := strconv.ParseInt(r.PathValue("eventId"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, r, ErrInvalidQuery)
		return
	}
	var body eventNoteJSON
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	if body.ExpectedVersion < 1 {
		writeError(w, r, ErrInvalidBody)
		return
	}
	result, err := h.events.DecideWithKey(
		r.Context(),
		principalFrom(r),
		id,
		body.ExpectedVersion,
		retry,
		body.Note,
		r.Header.Get("Idempotency-Key"),
	)
	if errors.Is(err, event.ErrConflict) {
		current := eventJSON(result)
		writeErrorWithEvent(w, r, err, &current)
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, eventJSON(result))
}

// eventJSON выдаёт только безопасные поля конверта. Произвольные payload и error
// могут содержать пароли, телефоны и токены: они остаются в служебном хранилище.
func eventJSON(value ports.ParkedEvent) parkedEventJSON {
	payload := map[string]any{}
	var raw map[string]json.RawMessage
	if json.Unmarshal(value.Value, &raw) == nil {
		for _, key := range []string{"version", "revision", "operation", "productId", "tagId"} {
			if field, ok := raw[key]; ok {
				var scalar any
				if json.Unmarshal(field, &scalar) == nil {
					switch scalar.(type) {
					case string, float64, bool:
						payload[key] = scalar
					}
				}
			}
		}
	}
	return parkedEventJSON{
		ID:         strconv.FormatInt(value.Control.ID, 10),
		Type:       value.Topic,
		TenantID:   value.TenantID,
		OccurredAt: value.ParkedAt,
		Attempts:   value.Attempts,
		Error:      value.Reason,
		Payload:    payload,
		Status:     value.Control.Status,
		Resolution: value.Control.Resolution,
		OperatorID: value.Control.OperatorID,
		UpdatedAt:  value.Control.UpdatedAt,
		Version:    value.Control.Version,
	}
}
