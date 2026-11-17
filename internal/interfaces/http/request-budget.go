package http

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

// requestBudget ограничивает работу обработчика, а не только запись ответа в сокет.
func requestBudget(path string, deps Deps, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		timeout := deps.RequestTimeout
		if timeout <= 0 {
			timeout = 25 * time.Second
		}
		budget := deps.DatabaseBudgets.Read
		switch {
		case strings.Contains(path, "/operator/"), strings.Contains(path, "/reference"):
			budget = deps.DatabaseBudgets.Background
		case strings.HasPrefix(path, "/api/v1/checkout"), strings.HasPrefix(path, "/api/v1/cart"),
			strings.HasPrefix(path, "/api/v1/payment-stub"):
			budget = deps.DatabaseBudgets.Checkout
		case r.Method != http.MethodGet && r.Method != http.MethodHead:
			budget = deps.DatabaseBudgets.Ingestion
		}
		// Импорт уже имеет отдельный проверенный бюджет и продлевает deadline записи ответа.
		if path == "/api/v1/operator/reference/import" {
			timeout = referenceImportTimeout
		}
		ctx, cancel := context.WithTimeout(ports.WithDatabaseBudget(r.Context(), budget), timeout)
		defer cancel()
		next(w, r.WithContext(ctx))
	}
}
