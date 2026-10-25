package http

import (
	"net/http"
	"net/url"
	"time"

	appbuyer "github.com/proshop-app/proshop-marketplace/internal/application/buyer"
)

func buyerOf(session appbuyer.IssuedSession) *buyerJSON {
	return &buyerJSON{
		ID:    string(session.Buyer.ID),
		Phone: string(session.Buyer.Phone),
		Exp:   session.AccessExpiresAt.Unix(),
	}
}

func setBuyerCookies(w http.ResponseWriter, session appbuyer.IssuedSession) {
	for name, value := range map[string]string{
		SessionCookie: session.Token,
		RefreshCookie: session.Key,
	} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    value,
			Path:     "/",
			Expires:  session.ExpiresAt,
			MaxAge:   max(1, int(time.Until(session.ExpiresAt).Seconds())),
			Secure:   true,
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
		})
	}

	clearBuyerCookie(w, staleSessionCookie)
	w.Header().Set("Cache-Control", "private, no-store")
}

func clearBuyerCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

func clearBuyerCookies(w http.ResponseWriter) {
	for _, name := range []string{SessionCookie, RefreshCookie, staleSessionCookie} {
		clearBuyerCookie(w, name)
	}
}

func refreshKey(r *http.Request) string {
	cookie, err := r.Cookie(RefreshCookie)
	if err != nil {
		return ""
	}

	return cookie.Value
}

func allowBuyerMutation(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		writeError(w, r, ErrForbidden)

		return false
	}

	if origin := r.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		// Прямые browser-запросы принимаются только с собственного HTTPS origin.
		// SSR проверяет origin на своей границе, backend получает запрос без него.
		if err != nil || parsed.Scheme != "https" || parsed.Host != r.Host || parsed.Path != "" ||
			parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			writeError(w, r, ErrForbidden)

			return false
		}
	}

	return true
}
