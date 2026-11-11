package moderation

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// RecoverProjectionCommand связывает восстановление с проверенными retained events
// и точными текущими версиями. Новый операторский допуск команда не создаёт.
type RecoverProjectionCommand struct {
	Previous               catalog.Snapshot
	Current                catalog.Snapshot
	ExpectedRevision       int64
	ExpectedVerdictVersion int64
	ApprovedVerdictVersion int64
	Apply                  bool
}

// RecoverProjectionResult пригоден для review до изменения и для технического аудита.
type RecoverProjectionResult struct {
	TenantID             ids.TenantID      `json:"tenantId"`
	ProductID            ids.ProductID     `json:"productId"`
	Operation            string            `json:"operation"`
	ApprovedRevision     int64             `json:"approvedRevision"`
	TargetRevision       int64             `json:"targetRevision"`
	BeforeVerdictVersion int64             `json:"beforeVerdictVersion"`
	AfterVerdictVersion  int64             `json:"afterVerdictVersion"`
	OriginalDecisionAt   time.Time         `json:"originalDecisionAt"`
	Operator             moderation.Actor  `json:"operator"`
	Status               moderation.Status `json:"status"`
	Applied              bool              `json:"applied"`
}

// RecoverProjection выполняет один явный CAS или только проверку. Доказательство
// source equality проверяется дополнительно к неизменяемому решению и карточке дела.
func (s *Service) RecoverProjection(
	ctx context.Context,
	cmd RecoverProjectionCommand,
) (RecoverProjectionResult, error) {
	var result RecoverProjectionResult
	if cmd.ExpectedRevision < 1 || cmd.ExpectedVerdictVersion < 1 || cmd.ApprovedVerdictVersion < 1 ||
		cmd.Current.Revision != cmd.ExpectedRevision || cmd.Previous.Revision < 1 ||
		cmd.Previous.Revision >= cmd.Current.Revision || cmd.Current.Revision-cmd.Previous.Revision != 1 ||
		cmd.Previous.TenantID != cmd.Current.TenantID || cmd.Previous.ProductID != cmd.Current.ProductID ||
		cmd.Previous.Deleted || cmd.Current.Deleted {
		return result, moderation.ErrInvalid
	}
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		definition, err := tx.Taxonomy().Current(ctx)
		if err != nil {
			return err
		}
		product, err := tx.Products().GetForUpdate(ctx, cmd.Current.TenantID, cmd.Current.ProductID)
		if err != nil {
			return err
		}
		p := product.State()
		if p.Revision != cmd.ExpectedRevision || p.Deleted || p.Card.Marketplace == nil {
			return moderation.ErrConflict
		}
		previous, err := catalog.NewProduct(cmd.Previous, s.clock.Now())
		if err != nil {
			return err
		}
		current, err := catalog.NewProduct(cmd.Current, s.clock.Now())
		if err != nil {
			return err
		}
		previous.Revalidate(definition, s.clock.Now())
		current.Revalidate(definition, s.clock.Now())
		if !catalog.IdenticalProjectionContent(previous.State(), current.State()) ||
			!catalog.IdenticalProjectionContent(current.State(), p) {
			return moderation.ErrCaseStale
		}
		verdict, err := tx.ModerationVerdicts().Get(ctx, p.TenantID, p.ProductID)
		if err != nil {
			return err
		}
		state := verdict.State()
		if state.Version != cmd.ExpectedVerdictVersion || state.Revision != p.Revision ||
			state.DecidedBy != moderation.Rule || state.Status != moderation.Pending || !probationOnly(state.Reasons) {
			return moderation.ErrConflict
		}
		approved, err := recoveryApproval(ctx, tx, state, cmd.ApprovedVerdictVersion)
		if err != nil {
			return err
		}
		if approved.Revision != cmd.Previous.Revision {
			return moderation.ErrCaseStale
		}
		member, err := tx.Participants().Get(ctx, p.TenantID)
		if err != nil {
			return err
		}
		rules, err := tx.ModerationRules().Current(ctx)
		if err != nil {
			return err
		}
		rules.RequireImage = s.config.RequireImage
		input := moderation.Input{
			Policy:      definition.ModerationPolicy(p.Card.Marketplace.CategoryID),
			Name:        p.Card.Name,
			Description: p.Card.Description,
			Image:       p.Card.Image,
			Probation:   member.State().Trust == participant.Probation,
		}
		changed, err := s.carryReviewedProjection(
			ctx,
			tx,
			verdict,
			approved,
			p.Revision,
			definition,
			rules,
			input,
			s.clock.Now(),
		)
		if err != nil {
			return err
		}
		if !changed {
			return moderation.ErrCaseStale
		}
		carried := verdict.State()
		result = RecoverProjectionResult{
			TenantID:             p.TenantID,
			ProductID:            p.ProductID,
			Operation:            "technical_carry_forward",
			ApprovedRevision:     approved.Revision,
			TargetRevision:       p.Revision,
			BeforeVerdictVersion: state.Version,
			AfterVerdictVersion:  carried.Version,
			OriginalDecisionAt:   approved.Decision.At,
			Operator:             approved.Operator,
			Status:               carried.Status,
			Applied:              cmd.Apply,
		}
		if !cmd.Apply {
			return nil
		}
		return s.saveEvaluation(ctx, tx, p, verdict, s.clock.Now())
	})
	return result, err
}
