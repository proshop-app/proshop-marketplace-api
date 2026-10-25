package http

import (
	"net/http"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/operator"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

func operatorKey(r *http.Request) string {
	cookie, err := r.Cookie(operatorKeyCookie)
	if err != nil {
		return ""
	}

	return cookie.Value
}

func setOperatorCookies(w http.ResponseWriter, access ports.IdentityAccess) {
	age := max(1, int(time.Until(access.ExpiresAt).Seconds()))
	setOperatorCookie(w, operatorCookie, access.Token, age)
	setOperatorCookie(w, operatorKeyCookie, access.Key, age)
}

func setOperatorCookie(w http.ResponseWriter, name, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    token,
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   maxAge,
	})
}

func clearOperatorCookies(w http.ResponseWriter) {
	for _, name := range []string{operatorCookie, operatorKeyCookie} {
		setOperatorCookie(w, name, "", -1)
	}
}

func (h *operatorHandlers) profileAllowed(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if h.origin == "" || (origin != "" && origin != h.origin) ||
		r.Header.Get("X-Requested-With") != "KvazarOperator" || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		writeError(w, r, operator.ErrForbidden)

		return false
	}

	return true
}
