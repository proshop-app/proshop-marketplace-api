package moderation

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

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

// ValidateSelector проверяет фильтры очереди; пустой отбор означает всю очередь.
func ValidateSelector(selector moderation.BulkSelector) error {
	if _, err := ids.ParseTenantID(string(selector.TenantID)); selector.TenantID != "" && err != nil {
		return moderation.ErrInvalid
	}
	if selector.After != "" {
		if _, err := ids.ParseUUID(string(selector.After)); err != nil {
			return moderation.ErrInvalid
		}
	}
	if len(selector.Reasons) > 20 ||
		(selector.Source != "" && selector.Source != moderation.Rule && selector.Source != moderation.Operator) ||
		(selector.Reason != "" && !validBulkReason(selector.Reason)) {
		return moderation.ErrInvalid
	}
	seen := map[string]bool{}
	for _, reason := range selector.Reasons {
		if !validBulkReason(reason) || seen[reason] {
			return moderation.ErrInvalid
		}
		seen[reason] = true
	}
	return nil
}

func validBulkReason(reason string) bool {
	return strings.HasPrefix(reason, "moderation.") && len(reason) <= 120
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
func (s *Service) BulkCount(ctx context.Context, selector moderation.BulkSelector) (moderation.BulkCounts, error) {
	if err := ValidateSelector(selector); err != nil {
		return moderation.BulkCounts{}, err
	}
	var count moderation.BulkCounts
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
	expires, err := bulkKeyExpiry(cmd, s.clock.Now())
	if err != nil {
		return result, err
	}
	key, fingerprint := bulkIdentity(cmd)
	err = s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		result, err = s.replayOrApprovePage(ctx, tx, cmd, key, fingerprint, expires)
		return err
	})
	return result, err
}

// Ключ несёт собственный срок. После удаления результата старый отбор никогда
// не исполняется снова: проверка срока не зависит от существования строки в БД.
func bulkKeyExpiry(cmd BulkCommand, now time.Time) (time.Time, error) {
	if cmd.Automatic {
		return now.Add(30 * 24 * time.Hour), nil
	}
	timestamp, nonce, ok := strings.Cut(cmd.IdempotencyKey, ":")
	milliseconds, err := strconv.ParseInt(timestamp, 10, 64)
	if !ok || nonce == "" || err != nil {
		return time.Time{}, moderation.ErrInvalid.WithMessage("ключ должен иметь формат unixMilliseconds:nonce")
	}
	created := time.UnixMilli(milliseconds)
	expires := created.Add(30 * 24 * time.Hour)
	if !now.Before(expires) || created.After(now.Add(5*time.Minute)) {
		return time.Time{}, moderation.ErrKeyExpired
	}
	return expires, nil
}

func bulkIdentity(cmd BulkCommand) (string, string) {
	identity, _ := json.Marshal([]string{"bulk.allow", cmd.Operator.ID, cmd.IdempotencyKey})
	raw, _ := json.Marshal(cmd)
	return fmt.Sprintf("%x", sha256.Sum256(identity)), fmt.Sprintf("%x", sha256.Sum256(raw))
}

func (s *Service) replayOrApprovePage(
	ctx context.Context,
	tx ports.Tx,
	cmd BulkCommand,
	key, fingerprint string,
	expires time.Time,
) (moderation.BulkResult, error) {
	result := moderation.BulkResult{Details: []moderation.BulkSkip{}}
	cached, found, err := tx.BulkModeration().Cached(ctx, key)
	if err != nil {
		return result, err
	}
	// Ключ мог истечь, пока запрос ждал сериализации; retention уже мог удалить результат.
	if !cmd.Automatic && !s.clock.Now().Before(expires) {
		return result, moderation.ErrKeyExpired
	}

	if found {
		if cached.Fingerprint != fingerprint {
			return result, moderation.ErrKeyReused
		}
		return cached.Result, nil
	}
	selected, err := selectPage(ctx, tx, cmd, &result)
	if err != nil {
		return result, err
	}
	if err := s.allowPage(ctx, tx, cmd, selected, &result); err != nil {
		return result, err
	}
	if err := s.enqueueTrustedContinuation(ctx, tx, cmd, result); err != nil {
		return result, err
	}
	err = tx.BulkModeration().Cache(ctx, key, ports.CachedBulkApproval{
		Fingerprint: fingerprint,
		Result:      result,
		ExpiresAt:   expires,
	})
	return result, err
}

func selectPage(
	ctx context.Context,
	tx ports.Tx,
	cmd BulkCommand,
	result *moderation.BulkResult,
) ([]moderation.BulkCase, error) {
	if cmd.Selector == nil {
		return cmd.Cases, nil
	}
	selected, err := tx.BulkModeration().Page(ctx, *cmd.Selector, BulkLimit)
	result.HasMore = len(selected) == BulkLimit
	if len(selected) > 0 {
		result.NextAfter = selected[len(selected)-1].ID
	}
	return selected, err
}

func (s *Service) enqueueTrustedContinuation(
	ctx context.Context,
	tx ports.Tx,
	cmd BulkCommand,
	result moderation.BulkResult,
) error {
	if !cmd.Automatic || !result.HasMore {
		return nil
	}
	return tx.Outbox().Enqueue(ctx, AllowTrustedTask(cmd.Selector.TenantID, result.NextAfter, s.clock.Now()))
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
	if cmd.Selector != nil && !matchesBulkSelector(c, *cmd.Selector) {
		return "other_reasons"
	}
	p := members[c.TenantID]
	if p == nil || (cmd.Automatic && p.State().Trust != participant.Trusted) {
		return "participant_changed"
	}
	return ""
}

func matchesBulkSelector(c moderation.CaseState, selector moderation.BulkSelector) bool {
	if (selector.TenantID != "" && c.TenantID != selector.TenantID) ||
		(selector.Source != "" && c.Source != selector.Source) ||
		(len(selector.Reasons) > 0 && !onlyReasons(c, selector.Reasons)) {
		return false
	}
	if selector.Reason == "" {
		return true
	}
	for _, reason := range c.Reasons {
		if reason.Key == selector.Reason {
			return true
		}
	}
	return false
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
