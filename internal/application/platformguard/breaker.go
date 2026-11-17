package platformguard

import (
	"errors"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

type breaker struct {
	failures   int
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
	if !failed {
		b.failures = 0
		b.openUntil = time.Time{}
		b.probe = false
		if p.probe {
			b.generation++
		}
		return
	}
	b.failures++
	if p.probe || b.failures >= cfg.FailureThreshold {
		b.openUntil = now.Add(cfg.OpenInterval)
		b.probe = false
		b.generation++
	}
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
