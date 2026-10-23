package http

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	appbuyer "github.com/proshop-app/proshop-marketplace/internal/application/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// SessionCookie — кука сессии покупателя: только HTTPS, только этот хост, без доступа из JS.
const SessionCookie = "__Host-mp_session"

// smsruAck — ответ, которого sms.ru ждёт от обработчика оповещений; иначе шлёт повторно.
const smsruAck = "100"

type authHandlers struct {
	auth       *appbuyer.Auth
	trustProxy bool
}

type startLoginJSON struct {
	Phone string `json:"phone"`
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
	var body startLoginJSON
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, r, err)

		return
	}

	view, err := h.auth.StartLogin(r.Context(), body.Phone, clientIP(r, h.trustProxy))
	if err != nil {
		writeError(w, r, err)

		return
	}

	writeJSON(w, http.StatusCreated, challengeOf(view))
}

// check — состояние проверки; подтверждённый звонок выдаёт сессию в куке.
func (h *authHandlers) check(w http.ResponseWriter, r *http.Request) {
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
		http.SetCookie(w, &http.Cookie{
			Name:     SessionCookie,
			Value:    session.Token,
			Path:     "/",
			Expires:  session.ExpiresAt,
			MaxAge:   int(time.Until(session.ExpiresAt).Seconds()),
			Secure:   true,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})

		// для клиента вход состоялся: внутреннее «проверка использована» наружу не выходит
		expires := session.ExpiresAt.UTC()
		out.Status = string(buyer.ChallengeConfirmed)
		out.Buyer = &buyerJSON{ID: string(session.Buyer.ID), Phone: string(session.Buyer.Phone)}
		out.SessionExpiresAt = &expires
	}

	writeJSON(w, http.StatusOK, out)
}

// logout — завершить сессию и стереть куку.
func (h *authHandlers) logout(w http.ResponseWriter, r *http.Request) {
	if err := h.auth.Logout(r.Context(), sessionToken(r)); err != nil {
		writeError(w, r, err)

		return
	}

	http.SetCookie(
		w,
		&http.Cookie{
			Name:     SessionCookie,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			Secure:   true,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		},
	)
	w.WriteHeader(http.StatusNoContent)
}

// me — покупатель сессии.
func (h *authHandlers) me(w http.ResponseWriter, r *http.Request) {
	found, err := h.auth.Me(r.Context(), BuyerID(r.Context()))
	if err != nil {
		writeError(w, r, err)

		return
	}

	writeJSON(w, http.StatusOK, buyerJSON{ID: string(found.ID), Phone: string(found.Phone)})
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

// clientIP — адрес покупателя: за доверенным прокси — первый в X-Forwarded-For.
func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			first, _, _ := strings.Cut(forwarded, ",")
			if ip := net.ParseIP(strings.TrimSpace(first)); ip != nil {
				return ip.String()
			}
		}
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}

	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}

	return ""
}
