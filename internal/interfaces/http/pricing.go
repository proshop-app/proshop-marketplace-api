package http

import (
	"net/http"

	appbuyer "github.com/proshop-app/proshop-marketplace/internal/application/buyer"
)

// optionalBuyer проверяет сессию до выбора цены. Недействительная сессия не становится гостевой молча.
func optionalBuyer(auth *appbuyer.Auth, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		if sessionToken(r) == "" {
			next(w, r)
			return
		}
		buyerOnly(auth, next)(w, r)
	}
}
