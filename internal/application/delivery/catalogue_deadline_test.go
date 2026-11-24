package delivery

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

func TestBackgroundCatalogueDeadlineIsRetryableUnavailable(t *testing.T) {
	service := NewService(fake.NewStore(), fake.NewPlatform(), fake.NewPickupCache(), Config{})
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	full := make(chan struct{}, 1)
	full <- struct{}{}
	pending := &flight{done: make(chan struct{}), cancel: func() {}}
	var err error
	pending.catalog, err = service.load(ctx, "key", "cdek", "Город", full)
	service.finishLoad(ctx, "key", "key", pending, err)
	if !errors.Is(pending.err, ErrSellerUnreachable) || errs.CodeOf(pending.err) != errs.Unavailable {
		t.Fatal("истечение общего срока должно отдавать повторяемый 503", pending.err)
	}
	if !pending.catalog.Failed || time.Until(pending.catalog.ExpiresAt) > pickupFailureTTL {
		t.Fatal("истечение срока стало пустым городом", pending.catalog)
	}
}
