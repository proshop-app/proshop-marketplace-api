// Package checkout — покупка, расчёты доставки и независимые заказы магазинов.
package checkout

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	appcart "github.com/proshop-app/proshop-marketplace/internal/application/cart"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// PointLookup — действующий пункт выдачи из справочника.
type PointLookup interface {
	Point(ctx context.Context, carrier delivery.Carrier, code string) (delivery.PickupPoint, error)
}

// Config — сроки расчётов.
type Config struct {
	// DeliverySubsidyMinPercent — нижняя граница субсидии продавца, 0–100.
	DeliverySubsidyMinPercent int
	// QuoteTTL — дольше этого цена доставки не держится, что бы ни обещал магазин.
	QuoteTTL      time.Duration
	ReturnBaseURL string
	CartTTL       time.Duration
	// QuoteTimeout — сколько ждать ответа магазина на расчёт.
	QuoteTimeout time.Duration
}

// Service — покупка покупателя.
type Service struct {
	uow      ports.UnitOfWork
	index    ports.OfferIndex
	platform ports.PlatformAPI
	points   PointLookup
	ids      ports.IDs
	clock    ports.Clock
	config   Config
}

// NewService — сценарии покупки.
func NewService(uow ports.UnitOfWork, index ports.OfferIndex, platform ports.PlatformAPI, points PointLookup,
	identities ports.IDs, clock ports.Clock, config Config) *Service {
	return &Service{
		uow:      uow,
		index:    index,
		platform: platform,
		points:   points,
		ids:      identities,
		clock:    clock,
		config:   config,
	}
}

// Create собирает покупку из корзины. Повтор с тем же ключом отдаёт ту же покупку.
func (s *Service) Create(ctx context.Context, buyerID ids.UUID, rawKey string) (checkout.State, error) {
	key, err := checkout.ParseIdempotencyKey(rawKey)
	if err != nil {
		return checkout.State{}, err
	}

	if existing, err := s.byKey(ctx, buyerID, key); err == nil || !errors.Is(err, checkout.ErrNotFound) {
		return existing, err
	}

	var lines []checkout.Line

	err = s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		c, err := tx.Carts().ByBuyer(ctx, buyerID)
		if errors.Is(err, cart.ErrNotFound) {
			return checkout.ErrEmpty
		}
		if err != nil {
			return err
		}
		if c.Expired(s.clock.Now()) {
			return checkout.ErrEmpty
		}

		for _, line := range c.Lines() {
			if line.Issue == cart.IssueUnavailable || line.Issue == cart.IssueNotEnoughStock {
				message := "в корзине есть недоступные позиции: " + line.Offer.String()

				return checkout.ErrOfferUnavailable.WithMessage(message)
			}

			lines = append(
				lines,
				checkout.Line{Offer: line.Offer, Name: line.Name, Quantity: line.Quantity, Price: line.Price},
			)
		}

		return nil
	})
	if err != nil {
		return checkout.State{}, err
	}

	if err := s.visible(ctx, lines); err != nil {
		return checkout.State{}, err
	}

	created, err := checkout.New(s.ids.NewUUID(), buyerID, key, lines, s.clock.Now())
	if err != nil {
		return checkout.State{}, err
	}

	err = s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.Checkouts().Add(ctx, created)
	})
	if errors.Is(err, checkout.ErrDuplicateKey) {
		return s.byKey(ctx, buyerID, key)
	}
	if err != nil {
		return checkout.State{}, err
	}

	return created.State(), nil
}

// Get — покупка покупателя.
func (s *Service) Get(ctx context.Context, buyerID, checkoutID ids.UUID) (checkout.State, error) {
	c, err := s.load(ctx, buyerID, checkoutID)
	if err != nil {
		return checkout.State{}, err
	}

	return c.State(), nil
}

// Quote выбирает пункт выдачи и спрашивает расчёт у каждого продавца
// покупки. Продавец без этого перевозчика получает расчёт «недоступно» с причиной.
func (s *Service) Quote(
	ctx context.Context,
	buyerID, checkoutID ids.UUID,
	carrier delivery.Carrier,
	pointCode string,
) (checkout.State, error) {
	c, err := s.load(ctx, buyerID, checkoutID)
	if err != nil {
		return checkout.State{}, err
	}
	if c.Status() != checkout.StatusDraft {
		return checkout.State{}, checkout.ErrNotDraft
	}

	point, err := s.points.Point(ctx, carrier, pointCode)
	if err != nil {
		return checkout.State{}, err
	}
	selection := checkout.Selection{
		Carrier:         carrier,
		PickupPointCode: point.Code,
		City:            point.City,
		Address:         point.Address,
		RegionCode:      point.RegionCode,
	}

	quotes, err := s.quote(ctx, buyerID, c.Groups(), selection)
	if err != nil {
		return checkout.State{}, err
	}

	return s.update(ctx, buyerID, checkoutID, func(c *checkout.Checkout, now time.Time) error {
		return c.SelectDelivery(selection, quotes, now)
	})
}

// Confirm перепроверяет цены и наличие у магазинов, пересчитывает
// устаревшую доставку и подтверждает покупку. Подтверждённая покупка
// отдаётся как есть. Изменились цены — покупка обновляется и ждёт
// подтверждения заново (checkout.ErrChanged).
func (s *Service) Confirm(ctx context.Context, buyerID, checkoutID ids.UUID) (checkout.State, error) {
	c, err := s.load(ctx, buyerID, checkoutID)
	if err != nil {
		return checkout.State{}, err
	}
	if c.Status() == checkout.StatusConfirmed {
		return c.State(), nil
	}
	if c.Delivery() == nil {
		return checkout.State{}, checkout.ErrDeliveryNotSelected
	}
	if undeliverable := c.Undeliverable(); len(undeliverable) > 0 {
		message := "не доставляют в выбранный пункт: " + joinTenants(undeliverable)

		return checkout.State{}, checkout.ErrDeliveryUnavailable.WithMessage(message)
	}

	checks, err := s.check(ctx, buyerID, c.Groups())
	if err != nil {
		return checkout.State{}, err
	}

	var changed bool

	repriced, err := s.update(ctx, buyerID, checkoutID, func(c *checkout.Checkout, now time.Time) error {
		var err error
		changed, err = c.Reprice(checks, now)

		return err
	})
	if err != nil {
		return checkout.State{}, err
	}
	if changed {
		return repriced, checkout.ErrChanged
	}

	c = checkout.Restore(repriced)
	stale := map[ids.TenantID]bool{}
	for _, tenant := range c.NeedsQuote(s.clock.Now()) {
		stale[tenant] = true
	}

	var requoted map[ids.TenantID]delivery.Quote

	if len(stale) > 0 {
		var groups []checkout.Group
		for _, group := range c.Groups() {
			if stale[group.TenantID] {
				groups = append(groups, group)
			}
		}

		if requoted, err = s.quote(ctx, buyerID, groups, *c.Delivery()); err != nil {
			return checkout.State{}, err
		}
	}

	var confirmErr error

	state, err := s.update(ctx, buyerID, checkoutID, func(c *checkout.Checkout, now time.Time) error {
		if len(requoted) > 0 {
			if err := c.Requote(requoted, now); err != nil {
				return err
			}
		}

		// расчёты сохраняются, даже если подтверждение не прошло: покупатель увидит причину
		confirmErr = c.Confirm(now)

		return nil
	})
	if err != nil {
		return checkout.State{}, err
	}

	return state, confirmErr
}

// quote спрашивает расчёт у продавцов параллельно, каждого — со своим сроком
// ожидания. Не ответивший продавец получает расчёт «магазин не ответил».
func (s *Service) quote(
	ctx context.Context,
	buyerID ids.UUID,
	groups []checkout.Group,
	selection checkout.Selection,
) (map[ids.TenantID]delivery.Quote, error) {
	subsidies, err := s.subsidies(ctx, groups)
	if err != nil {
		return nil, err
	}

	var (
		mu     sync.Mutex
		wg     sync.WaitGroup
		quotes = make(map[ids.TenantID]delivery.Quote, len(groups))
	)

	for _, group := range groups {
		wg.Add(1)

		go func(group checkout.Group) {
			defer wg.Done()
			quote := s.quoteGroup(ctx, buyerID, group, selection, subsidies[group.TenantID])

			mu.Lock()
			quotes[group.TenantID] = quote
			mu.Unlock()
		}(group)
	}

	wg.Wait()

	return quotes, nil
}

func (s *Service) quoteGroup(
	ctx context.Context,
	buyerID ids.UUID,
	group checkout.Group,
	selection checkout.Selection,
	settings participant.State,
) delivery.Quote {
	if !settings.Delivery.Allows(string(selection.Carrier), selection.RegionCode) {
		return delivery.Quote{
			Status:    delivery.QuoteUnavailable,
			Reason:    delivery.ReasonPickupPointUnavailable,
			ExpiresAt: s.clock.Now().Add(s.config.QuoteTTL),
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, s.config.QuoteTimeout)
	defer cancel()
	items := make([]ports.ItemRequest, 0, len(group.Lines))
	for _, line := range group.Lines {
		items = append(items, ports.ItemRequest{ProductID: line.Offer.Product, Quantity: line.Quantity})
	}

	offered, err := s.platform.QuoteDelivery(callCtx, group.TenantID, ports.QuoteRequest{
		BuyerID:         buyerID,
		Carrier:         selection.Carrier,
		PickupPointCode: selection.PickupPointCode,
		City:            selection.City,
		Items:           items,
	})
	now := s.clock.Now()
	if err != nil {
		return delivery.Unreachable(now)
	}
	apply := func(cost money.Money) (money.Money, error) {
		return group.DeliverySubsidy(
			settings.FreeDeliveryFrom,
			settings.Subsidy,
			cost,
			s.config.DeliverySubsidyMinPercent,
		)
	}

	quote, err := delivery.NewQuote(offered, apply, now, s.config.QuoteTTL)
	if err != nil {
		return delivery.Unreachable(now)
	}

	return quote
}

// subsidies — обязательные настройки продавцов; без участника доставка недоступна.
func (s *Service) subsidies(
	ctx context.Context,
	groups []checkout.Group,
) (map[ids.TenantID]participant.State, error) {
	subsidies := make(map[ids.TenantID]participant.State, len(groups))
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		for _, group := range groups {
			p, err := tx.Participants().Get(ctx, group.TenantID)
			if errors.Is(err, participant.ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}

			subsidies[group.TenantID] = p.State()
		}

		return nil
	})

	return subsidies, err
}

// check спрашивает у магазинов цену и наличие позиций покупки.
func (s *Service) check(
	ctx context.Context,
	buyerID ids.UUID,
	groups []checkout.Group,
) (map[ids.OfferID]checkout.PriceCheck, error) {
	cartGroups := make([]cart.Group, 0, len(groups))

	for _, group := range groups {
		cartGroup := cart.Group{TenantID: group.TenantID}
		for _, line := range group.Lines {
			cartGroup.Lines = append(
				cartGroup.Lines,
				cart.Line{Offer: line.Offer, Quantity: line.Quantity, Price: line.Price},
			)
		}

		cartGroups = append(cartGroups, cartGroup)
	}

	answers, err := appcart.CheckGroups(ctx, s.platform, buyerID, cartGroups)
	if err != nil {
		return nil, err
	}
	checks := make(map[ids.OfferID]checkout.PriceCheck, len(answers))
	for _, answer := range answers {
		checks[answer.Offer] = checkout.PriceCheck{
			Exists:    answer.Exists,
			Price:     answer.Price,
			Available: answer.Available,
		}
	}

	return checks, nil
}

// visible проверяет по индексу, что все позиции ещё открыты покупателю.
func (s *Service) visible(ctx context.Context, lines []checkout.Line) error {
	for _, line := range lines {
		doc, found, err := s.index.Get(ctx, line.Offer.String())
		if err != nil {
			return err
		}
		if !found || !doc.Visible {
			return checkout.ErrOfferUnavailable.WithMessage("предложение больше недоступно: " + line.Offer.String())
		}
		if !doc.CheckoutAvailable {
			message := "продавец ещё не готов к оформлению: " + line.Offer.String()

			return checkout.ErrOfferUnavailable.WithMessage(message)
		}
	}

	return nil
}

func (s *Service) load(ctx context.Context, buyerID, checkoutID ids.UUID) (*checkout.Checkout, error) {
	var found *checkout.Checkout
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		found, err = tx.Checkouts().Get(ctx, checkoutID)

		return err
	})
	if err != nil {
		return nil, err
	}
	if found.BuyerID() != buyerID {
		return nil, checkout.ErrNotFound
	}

	return found, nil
}

func (s *Service) byKey(ctx context.Context, buyerID ids.UUID, key string) (checkout.State, error) {
	var state checkout.State
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		found, err := tx.Checkouts().ByKey(ctx, buyerID, key)
		if err != nil {
			return err
		}

		state = found.State()

		return nil
	})

	return state, err
}

func (s *Service) update(
	ctx context.Context,
	buyerID, checkoutID ids.UUID,
	mutate func(*checkout.Checkout, time.Time) error,
) (checkout.State, error) {
	var state checkout.State
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		c, err := tx.Checkouts().Get(ctx, checkoutID)
		if err != nil {
			return err
		}
		if c.BuyerID() != buyerID {
			return checkout.ErrNotFound
		}
		before := c.State().Version
		if err := mutate(c, s.clock.Now()); err != nil {
			return err
		}

		if c.State().Version != before {
			if err := tx.Checkouts().Update(ctx, c); err != nil {
				return err
			}
		}

		state = c.State()

		return nil
	})

	return state, err
}

func joinTenants(tenants []ids.TenantID) string {
	parts := make([]string, 0, len(tenants))
	for _, tenant := range tenants {
		parts = append(parts, string(tenant))
	}

	return strings.Join(parts, ", ")
}

// DeliverySubsidyMinPercent — действующий порог для админки магазина.
func (s *Service) DeliverySubsidyMinPercent() int { return s.config.DeliverySubsidyMinPercent }
