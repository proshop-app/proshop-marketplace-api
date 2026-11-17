package platformguard

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

type operationClass int

const (
	checkoutClass operationClass = iota
	readClass
	classCount
)

func callerClass(ctx context.Context) operationClass {
	if ports.PlatformClassFromContext(ctx) == ports.PlatformCheckout {
		return checkoutClass
	}
	return readClass
}

func (c *Client) classLimit(class operationClass) int {
	if class == checkoutClass {
		return c.cfg.CheckoutMaxConcurrent
	}
	return c.cfg.ReadMaxConcurrent
}

func (c *Client) tenantLimit(class operationClass) int {
	// Доли магазинов тоже разделены: фоновые чтения не занимают резерв оформления.
	reads := max(1, c.cfg.TenantMaxConcurrent*c.cfg.ReadMaxConcurrent/c.cfg.MaxConcurrent)
	reads = min(reads, c.cfg.TenantMaxConcurrent-1)
	if class == readClass {
		return reads
	}
	return c.cfg.TenantMaxConcurrent - reads
}

func (class operationClass) String() string {
	if class == checkoutClass {
		return "checkout"
	}
	return "read"
}
