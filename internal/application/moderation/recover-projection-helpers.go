package moderation

import (
	"context"
	"reflect"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
)

func recoveryApproval(
	ctx context.Context,
	tx ports.Tx,
	current moderation.VerdictState,
	approvedVersion int64,
) (moderation.VerdictState, error) {
	history, err := tx.ModerationVerdicts().History(ctx, current.TenantID, current.ProductID)
	if err != nil {
		return moderation.VerdictState{}, err
	}
	return findRecoveryApproval(history, current, approvedVersion)
}

func findRecoveryApproval(
	history []moderation.VerdictState,
	current moderation.VerdictState,
	approvedVersion int64,
) (moderation.VerdictState, error) {
	if approvedVersion < 1 || approvedVersion >= current.Version ||
		current.Version-approvedVersion >= int64(len(history)) {
		return moderation.VerdictState{}, moderation.ErrCaseStale
	}
	versions := make(map[int64]moderation.VerdictState, len(history))
	for _, item := range history {
		if item.Version < approvedVersion {
			continue
		}
		if _, exists := versions[item.Version]; exists || item.Version > current.Version ||
			item.TenantID != current.TenantID || item.ProductID != current.ProductID {
			return moderation.VerdictState{}, moderation.ErrCaseStale
		}
		versions[item.Version] = item
		if item.Version > approvedVersion && (item.DecidedBy != moderation.Rule ||
			item.Status != moderation.Pending || item.ManualReject || !probationOnly(item.Reasons)) {
			return moderation.VerdictState{}, moderation.ErrCaseStale
		}
	}
	if !reflect.DeepEqual(versions[current.Version], current) {
		return moderation.VerdictState{}, moderation.ErrCaseStale
	}
	// History ограничена последними 50 записями: отсутствие промежуточной версии
	// не доказывает отсутствие последующего операторского вмешательства.
	for version := approvedVersion; version < current.Version; version++ {
		if _, found := versions[version]; !found {
			return moderation.VerdictState{}, moderation.ErrCaseStale
		}
	}
	return versions[approvedVersion], nil
}
