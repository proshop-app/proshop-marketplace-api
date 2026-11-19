package moderation

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/moderationstatus"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// BulkLimit ограничивает одновременно память, блокировки и размер транзакции.
const BulkLimit = 200

// BulkCommand принимает ровно один способ выбора и обязательный ключ повтора.
type BulkCommand struct {
	Cases          []moderation.BulkCase    `json:"cases,omitempty"`
	Selector       *moderation.BulkSelector `json:"selector,omitempty"`
	Note           string                   `json:"note"`
	Operator       moderation.Actor         `json:"-"`
	IdempotencyKey string                   `json:"-"`
	Automatic      bool                     `json:"-"`
}

// ValidateSelector не допускает неограниченного отбора без магазина и причин.
func ValidateSelector(selector moderation.BulkSelector) error {
	if _, err := ids.ParseTenantID(string(selector.TenantID)); err != nil {
		return moderation.ErrInvalid
	}
	if selector.After != "" {
		if _, err := ids.ParseUUID(string(selector.After)); err != nil {
			return moderation.ErrInvalid
		}
	}
	if len(selector.Reasons) == 0 || len(selector.Reasons) > 20 {
		return moderation.ErrInvalid
	}
	seen := map[string]bool{}
	for _, reason := range selector.Reasons {
		if !strings.HasPrefix(reason, "moderation.") || len(reason) > 120 || seen[reason] {
			return moderation.ErrInvalid
		}
		seen[reason] = true
	}
	return nil
}

func validateBulk(cmd BulkCommand) error {
	if strings.TrimSpace(cmd.IdempotencyKey) == "" || len(cmd.IdempotencyKey) > 200 ||
		strings.TrimSpace(cmd.Note) == "" || utf8.RuneCountInString(strings.TrimSpace(cmd.Note)) > 2000 ||
		(cmd.Selector == nil) == (len(cmd.Cases) == 0) || len(cmd.Cases) > BulkLimit {
		return moderation.ErrInvalid
	}
	if cmd.Selector != nil {
		return ValidateSelector(*cmd.Selector)
	}
	seen := map[ids.UUID]bool{}
	for _, c := range cmd.Cases {
		if _, err := ids.ParseUUID(string(c.ID)); err != nil || c.ExpectedVersion < 1 || seen[c.ID] {
			return moderation.ErrInvalid
		}
		seen[c.ID] = true
	}
	return nil
}

// BulkCount читает число дел с причинами только из набора для подтверждения оператора.
func (s *Service) BulkCount(ctx context.Context, selector moderation.BulkSelector) (int, error) {
	if err := ValidateSelector(selector); err != nil {
		return 0, err
	}
	var count int
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		count, err = tx.BulkModeration().Count(ctx, selector)
		return err
	})
	return count, err
}

// BulkAllow фиксирует страницу и её исходный ответ одной ограниченной транзакцией.
func (s *Service) BulkAllow(ctx context.Context, cmd BulkCommand) (moderation.BulkResult, error) {
	result := moderation.BulkResult{Details: []moderation.BulkSkip{}}
	if err := validateBulk(cmd); err != nil {
		return result, err
	}
	identity, _ := json.Marshal([]string{"bulk.allow", cmd.Operator.ID, cmd.IdempotencyKey})
	key := fmt.Sprintf("%x", sha256.Sum256(identity))
	raw, _ := json.Marshal(cmd)
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(raw))
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		cached, found, err := tx.BulkModeration().Cached(ctx, key)
		if err != nil {
			return err
		}
		if found {
			if cached.Fingerprint != fingerprint {
				return moderation.ErrKeyReused
			}
			result = cached.Result
			return nil
		}
		selected := cmd.Cases
		if cmd.Selector != nil {
			selected, err = tx.BulkModeration().Page(ctx, *cmd.Selector, BulkLimit)
			if err != nil {
				return err
			}
			result.HasMore = len(selected) == BulkLimit
			if len(selected) > 0 {
				result.NextAfter = selected[len(selected)-1].ID
			}
		}
		if err := s.allowPage(ctx, tx, cmd, selected, &result); err != nil {
			return err
		}
		if cmd.Automatic && result.HasMore {
			task := AllowTrustedTask(cmd.Selector.TenantID, result.NextAfter, s.clock.Now())
			if err := tx.Outbox().Enqueue(ctx, task); err != nil {
				return err
			}
		}
		return tx.BulkModeration().Cache(ctx, key, ports.CachedBulkApproval{
			Fingerprint: fingerprint,
			Result:      result,
		})
	})
	return result, err
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
	keys := make([]ids.UUID, 0, len(selected))
	for _, c := range selected {
		keys = append(keys, c.ID)
	}
	inputs, err := tx.BulkModeration().Read(ctx, keys)
	if err != nil {
		return err
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
		return err
	}
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
		state := input.Case.State()
		_, err := input.Case.Decide(item.ExpectedVersion, input.Revision, moderation.Allow, cmd.Note, now)
		if err != nil {
			return err
		}
		input.Verdict.Decide(moderation.Allow, "", now)
		input.Verdict.RecordDecision(cmd.Operator, moderation.VerdictDecision{
			Action: moderation.Allow,
			Note:   strings.TrimSpace(cmd.Note),
			At:     now,
		})
		changes = append(changes, ports.BulkModerationChange{
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
		})
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
	for _, tenant := range tenants {
		count := counts[tenant]
		if count == 0 {
			continue
		}
		p := members[tenant]
		trust := p.State().Trust
		p.ApproveOffers(count, s.config.ProbationOffers, now)
		if err := tx.Participants().Update(ctx, p); err != nil {
			return err
		}
		tasks = append(tasks, ports.StoreCardTask(tenant, now))
		if trust != p.State().Trust {
			if err := tx.ParticipantDecisions().Add(ctx, p.PromotionDecision(s.ids.NewUUID(), now)); err != nil {
				return err
			}
			tasks = append(tasks, moderationstatus.ParticipantTask(tenant, now), AllowTrustedTask(tenant, "", now))
		}
	}
	if len(changes) == 0 {
		return nil
	}
	return tx.BulkModeration().Save(ctx, changes, tasks)
}

func bulkSkipReason(
	input ports.BulkModerationInput,
	found bool,
	item moderation.BulkCase,
	cmd BulkCommand,
	members map[ids.TenantID]*participant.Participant,
) string {
	if !found {
		return "not_found"
	}
	c := input.Case.State()
	if c.Status != moderation.Open {
		return "already_decided"
	}
	if c.Version != item.ExpectedVersion || !input.Published || input.Verdict == nil ||
		c.Revision != input.Revision || input.Verdict.State().Revision != c.Revision {
		return "changed"
	}
	if cmd.Selector != nil && (c.TenantID != cmd.Selector.TenantID || !onlyReasons(c, cmd.Selector.Reasons)) {
		return "other_reasons"
	}
	p := members[c.TenantID]
	if p == nil || (cmd.Automatic && p.State().Trust != participant.Trusted) {
		return "participant_changed"
	}
	return ""
}

func onlyReasons(c moderation.CaseState, allowed []string) bool {
	if len(c.Reasons) == 0 {
		return false
	}
	for _, reason := range c.Reasons {
		if !slices.Contains(allowed, reason.Key) {
			return false
		}
	}
	return true
}

// allowTrustedProbation обрабатывает только одну страницу за аренду фоновой задачи.
func (s *Service) allowTrustedProbation(
	ctx context.Context,
	tenant ids.TenantID,
	after ids.UUID,
	taskID int64,
) error {
	trusted := false
	if err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		p, err := tx.Participants().Get(ctx, tenant)
		if err == nil {
			trusted = p.State().Trust == participant.Trusted
		}
		return err
	}); err != nil {
		return err
	}
	if !trusted {
		return nil
	}
	selector := moderation.BulkSelector{
		TenantID: tenant,
		Reasons:  []string{"moderation.probation"},
		After:    after,
	}
	_, err := s.BulkAllow(ctx, BulkCommand{
		Selector:       &selector,
		Note:           "Автоматическое одобрение после перехода участника в доверенные",
		Operator:       moderation.Actor{ID: "system", Name: "Автоматическое одобрение"},
		IdempotencyKey: fmt.Sprintf("trusted:%s:%d:%s", tenant, taskID, after),
		Automatic:      true,
	})
	return err
}
