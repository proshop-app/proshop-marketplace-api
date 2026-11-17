package http_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	transport "github.com/proshop-app/proshop-marketplace/internal/interfaces/http"
)

func TestLivenessDoesNotDependOnReadiness(t *testing.T) {
	e := newEnv(t)
	e.healthy = errors.New("база недоступна")
	if result := e.call(t, http.MethodGet, "/live", "", false); result.status != http.StatusOK {
		t.Fatal("процесс жив при отказе зависимости", result)
	}
	if result := e.call(t, http.MethodGet, "/ready", "", false); result.status != http.StatusServiceUnavailable {
		t.Fatal("витрина не готова без базы", result)
	}
}

func TestReadinessAllowsAuxiliaryFailuresButHealthReportsThem(t *testing.T) {
	for _, name := range []string{"redis", "kafka", "search"} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, func(deps *transport.Deps) {
				deps.Checks = append(deps.Checks, transport.Check{
					Name:     name,
					Optional: name != "search",
					Probe:    func(context.Context) error { return errors.New("нет ответа") },
				})
			})
			want := http.StatusOK
			if name == "search" {
				want = http.StatusServiceUnavailable
			}
			if result := e.call(t, http.MethodGet, "/ready", "", false); result.status != want {
				t.Fatal(result)
			}
			result := e.call(t, http.MethodGet, "/health", "", false)
			if result.status != http.StatusServiceUnavailable {
				t.Fatal(result)
			}
		})
	}
}
