package platformguard

import (
	"errors"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
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

func (b *breaker) finish(p breakerPermit, failed, neutral bool, cfg Config, now time.Time) {
	if p.generation != b.generation {
		return
	}
	if neutral {
		b.probe = false
		return
	}
	if p.probe {
		b.completeProbe(failed, cfg, now)
		return
	}
	b.window.record(now, cfg.FailureWindow, failed)
	b.failures++
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
