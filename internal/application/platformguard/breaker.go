package platformguard

import (
	"context"
	"errors"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

// verdict — чем завершённый вызов считается для размыкателя.
type verdict uint8

const (
	verdictSuccess verdict = iota
	verdictFailure
	// verdictSlow — магазин не ответил в срок. Общему серверу это доля окна, но не счёт подряд:
	// один зависший магазин подряд не размыкает сервер для всех, массовое зависание — размыкает.
	verdictSlow
	verdictNeutral
)

type breaker struct {
	failures   int
	window     failureWindow
	generation uint64
	openUntil  time.Time
	probe      bool
}

type breakerPermit struct {
	generation uint64
	probe      bool
}

func (b *breaker) available(now time.Time) bool {
	return b.openUntil.IsZero() || (!now.Before(b.openUntil) && !b.probe)
}

func (b *breaker) start() breakerPermit {
	p := breakerPermit{generation: b.generation, probe: !b.openUntil.IsZero()}
	b.probe = p.probe
	return p
}

func (b *breaker) finish(p breakerPermit, v verdict, cfg Config, now time.Time) {
	if p.generation != b.generation {
		return
	}
	if v == verdictNeutral {
		b.probe = false
		return
	}
	failed := v != verdictSuccess
	if p.probe {
		b.completeProbe(failed, cfg, now)
		return
	}
	b.window.record(now, cfg.FailureWindow, failed)
	if v != verdictSlow {
		b.failures++
	}
	if !failed {
		b.failures = 0
	}
	if b.failures >= cfg.FailureThreshold || b.window.exceeded(cfg.FailureMinCalls, cfg.FailurePercent) {
		b.open(cfg.OpenInterval, now)
	}
}

func (b *breaker) completeProbe(failed bool, cfg Config, now time.Time) {
	if failed {
		b.open(cfg.OpenInterval, now)
		return
	}
	b.failures = 0
	b.window = failureWindow{}
	b.openUntil = time.Time{}
	b.probe = false
	b.generation++
}

func (b *breaker) open(interval time.Duration, now time.Time) {
	b.openUntil = now.Add(interval)
	b.probe = false
	b.generation++
}

// serverVerdict отделяет отказ общего сервера от зависания одного магазина.
// Проба магазина, чей размыкатель уже открыт, общему серверу ничего не доказывает:
// иначе один зависший магазин раз в интервал добавлял бы сбой в окно всех магазинов.
// Короткий бюджет чтения (отзывы, сверка) тоже не доказывает: медленные отзывы под нагрузкой
// не должны закрывать оформление всем магазинам.
func serverVerdict(p permit, err error) verdict {
	var serverFailure *ports.PlatformServerFailure
	var catalogFailure *ports.PickupCatalogFailure
	switch {
	case errors.Is(err, context.Canceled):
		return verdictNeutral
	case errors.As(err, &serverFailure):
		return verdictFailure
	case !errors.Is(err, context.DeadlineExceeded):
		return verdictSuccess
	case errors.As(err, &catalogFailure) || p.shop.probe || !p.strict:
		return verdictNeutral
	default:
		return verdictSlow
	}
}

// tenantVerdict засчитывает магазину его отказы и зависание; общий сбой и справочник — нет.
func tenantVerdict(err error) verdict {
	var serverFailure *ports.PlatformServerFailure
	var catalogFailure *ports.PickupCatalogFailure
	if errors.Is(err, context.Canceled) || errors.As(err, &serverFailure) || errors.As(err, &catalogFailure) {
		return verdictNeutral
	}
	if tenantFailure(err) {
		return verdictFailure
	}
	return verdictSuccess
}

func tenantFailure(err error) bool {
	if err == nil || errors.Is(err, ports.ErrMarketplaceDisabled) {
		return false
	}
	var refusal *ports.OrderRefusal
	if errors.As(err, &refusal) && !refusal.Unexpected && refusal.Code != "MARKETPLACE_TEMPORARY_FAILURE" {
		return false
	}
	return errs.CodeOf(err) == errs.Unavailable || errs.CodeOf(err) == errs.Internal
}
