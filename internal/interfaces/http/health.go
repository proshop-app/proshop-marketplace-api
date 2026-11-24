package http

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/safego"
)

const checkTimeout = 2 * time.Second

type checkResult struct {
	check Check
	err   error
}

// health проверяет зависимости параллельно в общем бюджете. Вспомогательный
// отказ не закрывает готовую витрину, но остаётся видимым в диагностике.
func health(checks []Check, readiness bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), checkTimeout)
		defer cancel()
		results := make(chan checkResult, len(checks))
		for _, check := range checks {
			// Паника проверки — отказ зависимости, а не ожидание общего срока проверки.
			safego.Run(
				"http.health-"+check.Name,
				func() error {
					return check.Probe(ctx)
				},
				func(err error) {
					results <- checkResult{check: check, err: err}
				},
			)
		}
		body := healthJSON{Status: "ok", Checks: make(map[string]string, len(checks))}
		status := http.StatusOK
		for range checks {
			select {
			case result := <-results:
				recordCheck(r, &body, &status, result, readiness)
			case <-ctx.Done():
				for _, check := range checks {
					if _, found := body.Checks[check.Name]; !found {
						recordCheck(r, &body, &status, checkResult{check: check, err: ctx.Err()}, readiness)
					}
				}
				writeJSON(w, status, body)
				return
			}
		}
		writeJSON(w, status, body)
	}
}

func recordCheck(r *http.Request, body *healthJSON, status *int, result checkResult, readiness bool) {
	if result.err == nil {
		body.Checks[result.check.Name] = "ok"
		return
	}
	body.Checks[result.check.Name] = "fail"
	if !readiness || !result.check.Optional {
		body.Status, *status = "unavailable", http.StatusServiceUnavailable
	} else if *status == http.StatusOK {
		body.Status = "degraded"
	}
	loggerFrom(r.Context()).Warn(
		"health: зависимость не отвечает",
		slog.String("check", result.check.Name),
		slog.Any("error", result.err),
	)
}
