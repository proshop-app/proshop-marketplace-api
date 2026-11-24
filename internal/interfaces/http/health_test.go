package http_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	transport "github.com/proshop-app/proshop-marketplace/internal/interfaces/http"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

var errSearchDown = errs.New(errs.Unavailable, "search.unavailable", "поиск площадки недоступен")

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
					Optional: true,
					Probe:    func(context.Context) error { return errors.New("нет ответа") },
				})
			})
			if result := e.call(t, http.MethodGet, "/ready", "", false); result.status != http.StatusOK {
				t.Fatal(result)
			}
			result := e.call(t, http.MethodGet, "/health", "", false)
			if result.status != http.StatusServiceUnavailable {
				t.Fatal(result)
			}
		})
	}
}

func TestSearchOutageKeepsReadinessAndAnswersUnavailable(t *testing.T) {
	e := newEnv(t, func(deps *transport.Deps) {
		deps.Checks = append(deps.Checks, transport.Check{
			Name:     "search",
			Optional: true,
			Probe:    func(context.Context) error { return errSearchDown },
		})
	})
	e.index.BeforeGet = func(context.Context, string) error { return errSearchDown }

	if result := e.call(t, http.MethodGet, "/ready", "", false); result.status != http.StatusOK {
		t.Fatal("без поиска реплика остаётся в балансировке", result)
	}
	result := e.call(t, http.MethodGet, "/api/v1/catalog/offers/t1~p1", "", false)
	if result.status != http.StatusServiceUnavailable || errorCode(result) != "search.unavailable" {
		t.Fatal("выдача без поиска отвечает unavailable", result)
	}
}
