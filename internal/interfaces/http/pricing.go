package http

import (
	"context"
	"log/slog"
	"net/http"

	appbuyer "github.com/proshop-app/proshop-marketplace/internal/application/buyer"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// optionalBuyer допускает гостя; переданный токен должен быть действительным.
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

// publicBuyer допускает устаревший вход на публичных страницах.
func publicBuyer(auth *appbuyer.Auth, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		if sessionToken(r) == "" {
			next(w, r)
			return
		}
		session, err := auth.Authenticate(r.Context(), sessionToken(r))
		if err != nil {
			if errs.CodeOf(err) != errs.Unauthorized {
				writeError(w, r, err)
				return
			}
			next(w, r)
			return
		}
		next(w, withBuyer(r, session.BuyerID))
	}
}

func withBuyer(r *http.Request, id ids.UUID) *http.Request {
	rememberBuyer(r.Context(), id)
	logger := loggerFrom(r.Context()).With(slog.String("buyerId", string(id)))
	ctx := context.WithValue(r.Context(), keyBuyer, id)
	ctx = context.WithValue(ctx, keyLogger, logger)
	return r.WithContext(ctx)
}
