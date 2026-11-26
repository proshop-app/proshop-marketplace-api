package jobs

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/pkg/requestid"
)

func TestCheckoutReadFailureMetrics(t *testing.T) {
	var log bytes.Buffer
	registry := prometheus.NewRegistry()
	metrics := NewCheckoutMetrics(slog.New(slog.NewTextHandler(&log, nil)), registry)
	ctx := requestid.With(context.Background(), "req-1")
	metrics.RecordCheckoutReadFailure(ctx, "t1", "purchase", errors.New("ошибка разбора"), true)
	metrics.RecordCheckoutReadFailure(ctx, "t1", "next", errors.New("вне лимита журнала"), false)
	if line := log.String(); !strings.Contains(line, "tenantId=t1") ||
		!strings.Contains(line, "requestId=req-1") || !strings.Contains(line, "checkoutId=purchase") ||
		!strings.Contains(line, "ошибка разбора") {
		t.Fatalf("в журнале нет диагностики покупки: %s", line)
	}
	if strings.Contains(log.String(), "вне лимита журнала") {
		t.Fatalf("журнал превысил лимит: %s", log.String())
	}

	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) != 1 || families[0].GetName() != "marketplace_checkout_unreadable_total" ||
		len(families[0].Metric) != 1 {
		t.Fatalf("неожиданные метрики: %v", families)
	}
	metric := families[0].Metric[0]
	if metric.GetCounter().GetValue() != 2 || len(metric.Label) != 1 ||
		metric.Label[0].GetName() != "tenant" || metric.Label[0].GetValue() != "t1" {
		t.Fatalf("ошибка разбора не посчитана по магазину: %v", metric)
	}
}
