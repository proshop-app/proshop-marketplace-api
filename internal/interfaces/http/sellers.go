package http

import (
	"context"
	"log/slog"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func logSellerReadFailures(
	ctx context.Context,
	failures map[ids.TenantID]error,
	diagnostics *appcheckout.ReadDiagnostics,
) {
	for tenant, err := range failures {
		if !diagnostics.TakeLogSlot() {
			return
		}
		loggerFrom(ctx).ErrorContext(
			ctx,
			"не удалось прочитать продавца",
			slog.String("tenantId", string(tenant)),
			slog.Any("error", err),
		)
	}
}
