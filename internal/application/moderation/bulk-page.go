package moderation

import (
	"context"
	"slices"
	"strings"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/moderationstatus"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type approvalPage struct {
	changes []ports.BulkModerationChange
	tasks   []ports.NewTask
	counts  map[ids.TenantID]int
}

func (s *Service) allowPage(
	ctx context.Context,
	tx ports.Tx,
	cmd BulkCommand,
	selected []moderation.BulkCase,
	result *moderation.BulkResult,
) error {
	if len(selected) == 0 {
		return nil
	}
	inputs, tenants, members, err := loadPageInputs(ctx, tx, selected)
	if err != nil {
		return err
	}
	page, err := s.decidePage(tx, cmd, selected, inputs, members, result)
	if err != nil {
		return err
	}
	if err := s.applyParticipantApprovals(ctx, tx, tenants, members, &page); err != nil {
		return err
	}
	return persistPage(ctx, tx, page)
}

func loadPageInputs(
	ctx context.Context,
	tx ports.Tx,
	selected []moderation.BulkCase,
) (map[ids.UUID]ports.BulkModerationInput, []ids.TenantID, map[ids.TenantID]*participant.Participant, error) {
	keys := make([]ids.UUID, 0, len(selected))
	for _, c := range selected {
		keys = append(keys, c.ID)
	}
	inputs, err := tx.BulkModeration().Read(ctx, keys)
	if err != nil {
		return nil, nil, nil, err
	}
	tenants := []ids.TenantID{}
	for _, input := range inputs {
		tenant := input.Case.State().TenantID
		if !slices.Contains(tenants, tenant) {
			tenants = append(tenants, tenant)
		}
	}
	slices.Sort(tenants)
	members, err := tx.BulkModeration().Members(ctx, tenants)
	if err != nil {
		return nil, nil, nil, err
	}
	return inputs, tenants, members, nil
}

func (s *Service) decidePage(
	tx ports.Tx,
	cmd BulkCommand,
	selected []moderation.BulkCase,
	inputs map[ids.UUID]ports.BulkModerationInput,
	members map[ids.TenantID]*participant.Participant,
	result *moderation.BulkResult,
) (approvalPage, error) {
	changes, tasks := []ports.BulkModerationChange{}, []ports.NewTask{}
	counts := map[ids.TenantID]int{}
	now := s.clock.Now()
	for _, item := range selected {
		input, found := inputs[item.ID]
		reason := bulkSkipReason(input, found, item, cmd, members)
		if reason != "" {
			result.Details = append(result.Details, moderation.BulkSkip{ID: item.ID, Reason: reason})
			result.Skipped++
			continue
		}
		change, err := s.approveCase(input, item, cmd, now)
		if err != nil {
			return approvalPage{}, err
		}
		changes = append(changes, change)
		state := input.Case.State()
		counts[state.TenantID]++
		tasks = append(
			tasks,
			appcatalog.SyncOfferTask(state.TenantID, state.ProductID, now),
			moderationstatus.OfferTask(state.TenantID, state.ProductID, now),
		)
		s.recordVerdict(tx, input.Verdict.State())
		if s.config.Metrics != nil {
			tx.AfterCommit(func() { s.config.Metrics.Decision(moderation.Allow) })
		}
		result.Approved++
	}
	return approvalPage{changes: changes, tasks: tasks, counts: counts}, nil
}

func (s *Service) approveCase(
	input ports.BulkModerationInput,
	item moderation.BulkCase,
	cmd BulkCommand,
	now time.Time,
) (ports.BulkModerationChange, error) {
	state := input.Case.State()
	_, err := input.Case.Decide(item.ExpectedVersion, input.Revision, moderation.Allow, cmd.Note, now)
	if err != nil {
		return ports.BulkModerationChange{}, err
	}
	input.Verdict.Decide(moderation.Allow, "", now)
	input.Verdict.RecordDecision(cmd.Operator, moderation.VerdictDecision{
		Action: moderation.Allow,
		Note:   strings.TrimSpace(cmd.Note),
		At:     now,
	})
	return ports.BulkModerationChange{
		Case:    input.Case,
		Verdict: input.Verdict,
		Decision: moderation.Decision{
			ID:       s.ids.NewUUID(),
			CaseID:   state.ID,
			Operator: cmd.Operator,
			Action:   moderation.Allow,
			Note:     strings.TrimSpace(cmd.Note),
			Revision: state.Revision,
			At:       now,
		},
	}, nil
}

func (s *Service) applyParticipantApprovals(
	ctx context.Context,
	tx ports.Tx,
	tenants []ids.TenantID,
	members map[ids.TenantID]*participant.Participant,
	page *approvalPage,
) error {
	now := s.clock.Now()
	for _, tenant := range tenants {
		count := page.counts[tenant]
		if count == 0 {
			continue
		}
		p := members[tenant]
		trust := p.State().Trust
		p.ApproveOffers(count, s.config.ProbationOffers, now)
		if err := tx.Participants().Update(ctx, p); err != nil {
			return err
		}
		page.tasks = append(page.tasks, ports.StoreCardTask(tenant, now))
		if trust != p.State().Trust {
			if err := tx.ParticipantDecisions().Add(ctx, p.PromotionDecision(s.ids.NewUUID(), now)); err != nil {
				return err
			}
			page.tasks = append(
				page.tasks,
				moderationstatus.ParticipantTask(tenant, now),
				AllowTrustedTask(tenant, "", now),
			)
		}
	}
	return nil
}

func persistPage(ctx context.Context, tx ports.Tx, page approvalPage) error {
	if len(page.changes) == 0 {
		return nil
	}
	return tx.BulkModeration().Save(ctx, page.changes, page.tasks)
}
