package http

import (
	"net/http"
	"strings"

	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

const moneyUnitHeader = "X-Marketplace-Money-Unit"

var errMoneyUnitRequired = errs.New(
	errs.Conflict,
	"api.money_unit_required",
	"обновите приложение: денежный контракт требует X-Marketplace-Money-Unit: RUB",
)

// moneyContract не даёт старой открытой витрине прочитать или отправить суммы в другой единице.
func moneyContract(path string, next http.HandlerFunc) http.HandlerFunc {
	if !strings.HasPrefix(path, "/api/v1/") || strings.HasPrefix(path, "/api/v1/internal/") {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(moneyUnitHeader, "RUB")
		if publicMoneyRoute(r.Method, path) {
			w.Header().Add("Vary", moneyUnitHeader)
			if values := r.Header.Values(moneyUnitHeader); len(values) != 1 || values[0] != "RUB" {
				w.Header().Set("Cache-Control", "no-store")
				writeError(w, r, errMoneyUnitRequired)
				return
			}
		}
		next(w, r)
	}
}

// Возвраты из платёжной формы и прочие неденежные маршруты работают без нового заголовка.
func publicMoneyRoute(method, path string) bool {
	if strings.HasPrefix(path, "/api/v1/checkout/") &&
		(strings.HasSuffix(path, "/return") || strings.HasSuffix(path, "/pay")) {
		return false
	}
	if strings.HasPrefix(path, "/api/v1/catalog/offers/") && strings.HasSuffix(path, "/reviews") {
		return false
	}
	if strings.HasPrefix(path, "/api/v1/shops/") {
		return false
	}
	if strings.HasPrefix(path, "/api/v1/buyer/favorites") ||
		strings.HasPrefix(path, "/api/v1/buyer/subscriptions/shops") {
		return method == http.MethodGet
	}
	if strings.HasPrefix(path, "/api/v1/buyer/price-watches") {
		return method != http.MethodDelete
	}
	if strings.HasPrefix(path, "/api/v1/operator/participants/") && strings.HasSuffix(path, "/decisions") {
		return false
	}
	if strings.HasPrefix(path, "/api/v1/operator/moderation/") {
		for _, prefix := range []string{"cases", "offers", "verdicts"} {
			if strings.HasPrefix(path, "/api/v1/operator/moderation/"+prefix) {
				return true
			}
		}
		return false
	}
	if strings.HasPrefix(path, "/api/v1/operator/promotion/") {
		if method == http.MethodDelete || strings.HasSuffix(path, "/stats") ||
			strings.HasSuffix(path, "/calendar") ||
			(method == http.MethodGet && strings.HasSuffix(path, "/decisions")) {
			return false
		}
		return true
	}
	for _, prefix := range []string{
		"/api/v1/catalog/offers",
		"/api/v1/catalog/sponsored",
		"/api/v1/recommendations",
		"/api/v1/cart",
		"/api/v1/checkout",
		"/api/v1/orders",
		"/api/v1/shops",
		"/api/v1/operator/participants",
		"/api/v1/operator/checkouts",
	} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}
