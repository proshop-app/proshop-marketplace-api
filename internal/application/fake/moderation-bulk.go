package fake

import (
	"context"
	"slices"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type bulkModeration struct{ d *data }

func (t *tx) BulkModeration() ports.BulkModeration { return bulkModeration{d: t.data} }

func bulkMatches(c moderation.CaseState, selector moderation.BulkSelector) bool {
	if c.Status != moderation.Open || (selector.TenantID != "" && c.TenantID != selector.TenantID) ||
		(selector.Source != "" && c.Source != selector.Source) {
		return false
	}
	found := selector.Reason == ""
	for _, reason := range c.Reasons {
		found = found || reason.Key == selector.Reason
		if len(selector.Reasons) > 0 && !slices.Contains(selector.Reasons, reason.Key) {
			return false
		}
	}
	return found && (len(selector.Reasons) == 0 || len(c.Reasons) > 0)
}

func (r bulkModeration) Page(
	_ context.Context,
	selector moderation.BulkSelector,
	limit int,
) ([]moderation.BulkCase, error) {
	result := []moderation.BulkCase{}
	for _, c := range r.d.moderation.cases {
		if bulkMatches(c, selector) && c.ID > selector.After {
			result = append(result, moderation.BulkCase{ID: c.ID, ExpectedVersion: c.Version})
		}
	}
	slices.SortFunc(result, func(a, b moderation.BulkCase) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return result[:min(limit, len(result))], nil
}

func (r bulkModeration) Count(_ context.Context, selector moderation.BulkSelector) (moderation.BulkCounts, error) {
	count := moderation.BulkCounts{}
	tenants := map[ids.TenantID]bool{}
	for _, c := range r.d.moderation.cases {
		if bulkMatches(c, selector) {
			count.Total++
			tenants[c.TenantID] = true
			if len(c.Reasons) != 1 || c.Reasons[0].Key != "moderation.probation" {
				count.OtherReasons++
			}
		}
	}
	count.Tenants = len(tenants)
	return count, nil
}

func (r bulkModeration) Read(
	_ context.Context,
	keys []ids.UUID,
) (map[ids.UUID]ports.BulkModerationInput, error) {
	result := map[ids.UUID]ports.BulkModerationInput{}
	for _, key := range keys {
		c, found := r.d.moderation.cases[key]
		if !found {
			continue
		}
		offer := productKey{c.TenantID, c.ProductID}
		product := r.d.products[offer]
		input := ports.BulkModerationInput{
			Case:      moderation.RestoreCase(c),
			Revision:  product.Revision,
			Published: !product.Deleted && product.Card.Marketplace != nil && product.Card.Publication.Published,
		}
		if v, ok := r.d.moderation.verdicts[offer]; ok {
			input.Verdict = moderation.RestoreVerdict(v)
		}
		result[key] = input
	}
	return result, nil
}

func (r bulkModeration) Members(
	ctx context.Context,
	tenants []ids.TenantID,
) (map[ids.TenantID]*participant.Participant, error) {
	return participants(r).Batch(ctx, tenants)
}

func (r bulkModeration) Save(
	ctx context.Context,
	changes []ports.BulkModerationChange,
	tasks []ports.NewTask,
) error {
	for _, change := range changes {
		if err := (moderationVerdicts{d: r.d.moderation, store: r.d}).Save(ctx, change.Verdict); err != nil {
			return err
		}
		if err := (moderationCases{d: r.d.moderation}).Save(ctx, change.Case); err != nil {
			return err
		}
		if err := (moderationDecisions{d: r.d.moderation}).Add(ctx, change.Decision); err != nil {
			return err
		}
	}
	for _, task := range tasks {
		if err := outbox(r).Enqueue(ctx, task); err != nil {
			return err
		}
	}
	return nil
}

func (r bulkModeration) Cached(_ context.Context, key string) (ports.CachedBulkApproval, bool, error) {
	value, found := r.d.moderation.cachedBulk[key]
	return value, found, nil
}

func (r bulkModeration) Cache(_ context.Context, key string, value ports.CachedBulkApproval) error {
	r.d.moderation.cachedBulk[key] = value
	return nil
}
