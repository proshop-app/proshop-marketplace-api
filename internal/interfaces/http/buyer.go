package http

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	appbuyer "github.com/proshop-app/proshop-marketplace/internal/application/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// SessionCookie — кука сессии покупателя: только HTTPS, только этот хост, без доступа из JS.
const (
	SessionCookie      = "__Host-mp_access"
	RefreshCookie      = "__Host-mp_key"
	staleSessionCookie = "__Host-mp_session"
)

// smsruAck — ответ, которого sms.ru ждёт от обработчика оповещений; иначе шлёт повторно.
const smsruAck = "100"

type authHandlers struct {
	auth           *appbuyer.Auth
	trustedProxies int
}

type startLoginJSON struct {
	Phone string `json:"phone"`
	Exp   int64  `json:"exp,omitempty"`
}

type checkLoginJSON struct {
	ChallengeID string `json:"challengeId"`
}

type challengeJSON struct {
	ChallengeID     string    `json:"challengeId"`
	Status          string    `json:"status"`
	CallPhone       string    `json:"callPhone,omitempty"`
	CallPhonePretty string    `json:"callPhonePretty,omitempty"`
	ExpiresAt       time.Time `json:"expiresAt"`
}

type loginJSON struct {
	challengeJSON
	Buyer            *buyerJSON `json:"buyer,omitempty"`
	SessionExpiresAt *time.Time `json:"sessionExpiresAt,omitempty"`
}

type buyerJSON struct {
	Exp   int64  `json:"exp,omitempty"`
	ID    string `json:"id"`
	Phone string `json:"phone"`
}

// BuyerID — покупатель сессии из контекста запроса.
func BuyerID(ctx context.Context) ids.UUID {
	id, _ := ctx.Value(keyBuyer).(ids.UUID)

	return id
}

func challengeOf(view appbuyer.ChallengeView) challengeJSON {
	return challengeJSON{
		ChallengeID:     string(view.ID),
		Status:          string(view.Status),
		CallPhone:       view.CallPhone,
		CallPhonePretty: view.CallPhonePretty,
		ExpiresAt:       view.ExpiresAt.UTC(),
	}
}

// start — начать вход: POST /api/v1/auth/call {phone}.
func (h *authHandlers) start(w http.ResponseWriter, r *http.Request) {
	if !allowBuyerMutation(w, r) {
		return
	}
	var body startLoginJSON
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, r, err)

		return
	}

	view, err := h.auth.StartLogin(r.Context(), body.Phone, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)

		return
	}

	writeJSON(w, http.StatusCreated, challengeOf(view))
}

// check — состояние проверки; подтверждённый звонок выдаёт сессию в куке.
func (h *authHandlers) check(w http.ResponseWriter, r *http.Request) {
	if !allowBuyerMutation(w, r) {
		return
	}
	var body checkLoginJSON
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, r, err)

		return
	}

	challengeID, err := ids.ParseUUID(body.ChallengeID)
	if err != nil {
		writeError(w, r, buyer.ErrChallengeNotFound)

		return
	}

	result, err := h.auth.CheckLogin(r.Context(), challengeID)
	if err != nil {
		writeError(w, r, err)

		return
	}

	out := loginJSON{challengeJSON: challengeOf(result.Challenge)}

	if session := result.Session; session != nil {
		setBuyerCookies(w, *session)

		// для клиента вход состоялся: внутреннее «проверка использована» наружу не выходит
		expires := session.AccessExpiresAt.UTC()
		out.Status = string(buyer.ChallengeConfirmed)
		out.Buyer = buyerOf(*session)
		out.SessionExpiresAt = &expires
	}

	writeJSON(w, http.StatusOK, out)
}

// logout закрывает только refresh; JWT остаётся действителен до exp.
func (h *authHandlers) logout(w http.ResponseWriter, r *http.Request) {
	if !allowBuyerMutation(w, r) {
		return
	}

	err := h.auth.Logout(r.Context(), refreshKey(r))
	clearBuyerCookies(w)
	if err != nil {
		writeError(w, r, err)

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// refresh доступен только по отдельному HttpOnly ключу, не по access JWT.
func (h *authHandlers) refresh(w http.ResponseWriter, r *http.Request) {
	if !allowBuyerMutation(w, r) {
		return
	}

	session, err := h.auth.Refresh(r.Context(), refreshKey(r))
	if err != nil {
		writeError(w, r, err)

		return
	}

	setBuyerCookies(w, session)
	writeJSON(w, http.StatusOK, buyerOf(session))
}

// me — единственная GET-ручка, допускающая silent refresh при загрузке.
func (h *authHandlers) me(w http.ResponseWriter, r *http.Request) {
	// Не разрешаем cross-site загрузкой вращать cookie через GET.
	if !allowBuyerMutation(w, r) {
		return
	}

	session, err := h.auth.Bootstrap(r.Context(), sessionToken(r), refreshKey(r))
	if err != nil {
		writeError(w, r, err)

		return
	}

	if session.Key != "" {
		setBuyerCookies(w, session)
	}

	writeJSON(w, http.StatusOK, buyerOf(session))
}

// smsruWebhook — оповещение sms.ru: форма с записями data[N] вида
// «callcheck_status\n<check_id>\n<код>\n<время>». Телу не верим: состояние
// перечитывается у шлюза. Ответ «100» — иначе шлюз повторяет доставку.
func (h *authHandlers) smsruWebhook(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := r.ParseForm(); err != nil {
		writeError(w, r, ErrInvalidBody.Wrap(err))

		return
	}

	for key, values := range r.PostForm {
		if !strings.HasPrefix(key, "data[") {
			continue
		}

		for _, entry := range values {
			lines := strings.Split(strings.ReplaceAll(entry, "\r", ""), "\n")
			if len(lines) < 2 || strings.TrimSpace(lines[0]) != "callcheck_status" ||
				strings.TrimSpace(lines[1]) == "" {
				continue
			}

			if err := h.auth.RefreshByProvider(r.Context(), strings.TrimSpace(lines[1])); err != nil {
				loggerFrom(r.Context()).Warn("sms.ru: оповещение не обработано", slog.Any("error", err))
			}
		}
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(smsruAck))
}

// buyerOnly пропускает запрос с действующей сессией покупателя: кука
// __Host-mp_session или заголовок Authorization: Bearer (серверный рендер
// витрины пересылает сессию заголовком).
func buyerOnly(auth *appbuyer.Auth, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !allowBuyerMutation(w, r) {
			return
		}

		session, err := auth.Authenticate(r.Context(), sessionToken(r))
		if err != nil {
			writeError(w, r, err)

			return
		}

		rememberBuyer(r.Context(), session.BuyerID)

		logger := loggerFrom(r.Context()).With(slog.String("buyerId", string(session.BuyerID)))
		ctx := context.WithValue(r.Context(), keyBuyer, session.BuyerID)
		ctx = context.WithValue(ctx, keyLogger, logger)

		next(w, r.WithContext(ctx))
	}
}

func sessionToken(r *http.Request) string {
	if cookie, err := r.Cookie(SessionCookie); err == nil && cookie.Value != "" {
		return cookie.Value
	}

	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")

	return strings.TrimSpace(token)
}
