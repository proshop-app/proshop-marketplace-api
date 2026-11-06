// Package promotion выполняет сценарии продвижения через транзакционные порты.
package promotion

import (
	"context"
	"errors"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/promotion"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// Config передаёт календарь, ротацию и внешние зависимости без чтения окружения.
type Config struct {
	Catalog         *appcatalog.Queries
	Location        *time.Location
	SearchDepth     int
	VisitorSalt     string
	Random          promotion.Random
	Metrics         ports.PromotionMetrics
	Limiter         ports.RateLimiter
	EventsPerMinute int
}

// Service связывает изменения продвижения одной транзакцией.
type Service struct {
	uow    ports.UnitOfWork
	clock  ports.Clock
	ids    ports.IDs
	index  ports.OfferIndex
	config Config
}

// Command защищает решение ожидаемой версией размещения.
type Command struct {
	Action          promotion.Action
	ExpectedVersion int64
	Note            string
	ERID            string
	Payment         promotion.Payment
}

// Access ограничивает машинный API одним магазином.
type Access struct {
	TenantID ids.TenantID
	Actor    string
}

// NewService получает зависимости через порты.
func NewService(
	uow ports.UnitOfWork,
	clock ports.Clock,
	identities ports.IDs,
	index ports.OfferIndex,
	config Config,
) *Service {
	if config.Random == nil {
		panic("promotion: необходим Random")
	}
	return &Service{uow: uow, clock: clock, ids: identities, index: index, config: config}
}

func (s *Service) prepare(ctx context.Context, tx ports.Tx, input *promotion.CampaignState) error {
	if input.Target.OfferID.Tenant != input.TenantID {
		return promotion.ErrNotFound
	}
	offer, err := tx.Offers().Offer(ctx, input.TenantID, input.Target.OfferID.Product)
	if errors.Is(err, catalog.ErrProductNotFound) {
		return promotion.ErrNotFound
	}
	if err != nil {
		return err
	}
	doc, ok := appcatalog.Document(offer)
	if !ok || !doc.Listed {
		return promotion.ErrInvalid
	}
	card := offer.Product.State().Card.Marketplace
	if card == nil || card.CategoryID == "" {
		return promotion.ErrOfferWithoutCategory
	}
	input.Target.CategoryID = card.CategoryID
	if input.Status != "" && input.Status != promotion.Draft {
		return nil
	}
	p, err := tx.Promotion().Placement(ctx, input.PlacementCode)
	if err != nil {
		return err
	}
	weeks, err := input.Period.Weeks(s.config.Location)
	if err != nil {
		return err
	}
	input.Price, err = p.Quote(input.Position, len(weeks))
	return err
}

// Create проверяет публикацию и фиксирует цену из прайса в черновике.
func (s *Service) Create(
	ctx context.Context,
	access Access,
	input promotion.CampaignState,
) (Campaign, error) {
	var result Campaign
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		if access.TenantID != "" {
			input.TenantID = access.TenantID
		}
		if access.TenantID != "" && input.Marking.ERID != "" {
			return promotion.ErrInvalid
		}
		input.Status = promotion.Draft
		if err := s.prepare(ctx, tx, &input); err != nil {
			return err
		}
		input.ID = s.ids.NewUUID()
		campaign, err := promotion.NewCampaign(input, s.clock.Now(), s.config.Location)
		if err != nil {
			return err
		}
		if err := tx.Promotion().Save(ctx, campaign); err != nil {
			return err
		}
		result, err = campaignView(ctx, tx, campaign.State())
		return err
	})
	return result, err
}

func owned(ctx context.Context, tx ports.Tx, access Access, id ids.UUID) (*promotion.Campaign, error) {
	c, err := tx.Promotion().Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if access.TenantID != "" && c.State().TenantID != access.TenantID {
		return nil, promotion.ErrNotFound
	}
	return c, nil
}

func (s *Service) capacity(ctx context.Context, tx ports.Tx, state promotion.CampaignState) error {
	if err := tx.Promotion().LockPlacement(ctx, state.PlacementCode); err != nil {
		return err
	}
	p, err := tx.Promotion().Placement(ctx, state.PlacementCode)
	if err != nil {
		return err
	}
	if !p.State().Active {
		return promotion.ErrInvalid
	}
	weeks, err := state.Period.Weeks(s.config.Location)
	if err != nil {
		return err
	}
	occupancy, err := tx.Promotion().Occupancy(ctx, state, p.State().Scope, weeks, state.ID)
	if err != nil {
		return err
	}
	return promotion.CheckCapacity(occupancy, p.State().SlotLimit)
}

// Edit проверяет редактируемое состояние до изменения агрегата.
func (s *Service) Edit(
	ctx context.Context,
	access Access,
	id ids.UUID,
	expected int64,
	input promotion.CampaignState,
) (Campaign, error) {
	var result Campaign
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		c, err := owned(ctx, tx, access, id)
		if err != nil {
			return err
		}
		if expected != c.State().Version {
			return promotion.ErrConflict
		}
		if access.TenantID != "" && c.State().Status != promotion.Draft {
			return promotion.ErrConflict
		}
		input.TenantID = c.State().TenantID
		input.Status = c.State().Status
		input.Price = c.State().Price
		if access.TenantID != "" && input.Marking.ERID != "" {
			return promotion.ErrInvalid
		}
		if err := s.prepare(ctx, tx, &input); err != nil {
			return err
		}
		changed, err := c.Edit(input, s.clock.Now(), s.config.Location)
		if err != nil {
			return err
		}
		if changed {
			if promotion.Occupies(c.State().Status) {
				if err := s.capacity(ctx, tx, c.State()); err != nil {
					return err
				}
			}
			if err := tx.Promotion().Save(ctx, c); err != nil {
				return err
			}
		}
		result, err = campaignView(ctx, tx, c.State())
		return err
	})
	return result, err
}

// Decide проверяет переход и фиксирует решение без повторного изменения состояния.
func (s *Service) Decide(
	ctx context.Context,
	access Access,
	id ids.UUID,
	command Command,
) (Campaign, error) {
	var result Campaign
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		c, err := owned(ctx, tx, access, id)
		if err != nil {
			return err
		}
		if c.State().Version != command.ExpectedVersion {
			return promotion.ErrConflict
		}
		if access.TenantID != "" && command.Action != promotion.Submit &&
			command.Action != promotion.Stop && command.Action != promotion.Revise {
			return promotion.ErrActionUnsupported
		}
		if command.Action == promotion.Submit {
			p, err := tx.Participants().Get(ctx, c.State().TenantID)
			if err != nil {
				return err
			}
			if !p.CardReady() {
				return promotion.ErrCardNotReady
			}
		}
		if command.Action == promotion.Submit && c.State().Status == promotion.Draft {
			if err := c.State().Period.ValidateStart(s.clock.Now(), s.config.Location); err != nil {
				return err
			}
			_, listed, err := currentOffer(ctx, tx, c.State())
			if err != nil {
				return err
			}
			if !listed {
				return promotion.ErrInvalid
			}
		}
		before := c.State().Status
		if command.Action == promotion.Submit || command.Action == promotion.Approve {
			if err := c.State().Marking.Validate(true); err != nil {
				return err
			}
			if err := s.capacity(ctx, tx, c.State()); err != nil {
				return err
			}
		}
		if command.ERID == "" {
			command.ERID = c.State().Marking.ERID
		}
		changed, err := c.Decide(
			command.Action,
			command.Note,
			command.ERID,
			command.Payment,
			s.clock.Now().In(s.config.Location),
		)
		if err != nil {
			return err
		}
		if changed {
			if err := s.saveDecision(ctx, tx, c, before, access.Actor, command.Action, command.Note); err != nil {
				return err
			}
		}
		result, err = campaignView(ctx, tx, c.State())
		return err
	})
	return result, err
}

func (s *Service) saveDecision(
	ctx context.Context,
	tx ports.Tx,
	c *promotion.Campaign,
	before promotion.Status,
	actor string,
	action promotion.Action,
	note string,
) error {
	if err := tx.Promotion().Save(ctx, c); err != nil {
		return err
	}
	d := promotion.Decision{At: s.clock.Now(), Actor: actor, Action: action, Note: note}
	if err := tx.Promotion().AddDecision(ctx, c.State().ID, d); err != nil {
		return err
	}
	if s.config.Metrics != nil {
		state := c.State()
		tx.AfterCommit(func() { s.config.Metrics.PromotionTransition(state, before) })
	}
	return nil
}

// ConfigurePlacement сериализует правку прайса с резервированием слотов.
func (s *Service) ConfigurePlacement(
	ctx context.Context,
	code string,
	limit int,
	price money.Money,
	prices map[int]money.Money,
	active bool,
	expectedVersion int64,
) (promotion.PlacementState, error) {
	var result promotion.PlacementState
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		if err := tx.Promotion().LockPlacement(ctx, code); err != nil {
			return err
		}
		p, err := tx.Promotion().Placement(ctx, code)
		if err != nil {
			return err
		}
		if p.State().Version != expectedVersion {
			return promotion.ErrConflict
		}
		changed, err := p.Configure(limit, price, prices, active)
		if err != nil {
			return err
		}
		if changed {
			if err := tx.Promotion().SavePlacement(ctx, p); err != nil {
				return err
			}
		}
		result = p.State()
		return nil
	})
	return result, err
}
