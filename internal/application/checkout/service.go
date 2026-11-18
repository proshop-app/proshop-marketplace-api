// Package checkout — покупка, расчёты доставки и независимые заказы магазинов.
package checkout

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	appcart "github.com/proshop-app/proshop-marketplace/internal/application/cart"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

const updateRetryDelay = 10 * time.Millisecond

// Config — сроки расчётов.
type Config struct {
	// OrderCallTimeout — уже настроенный HTTP-бюджет адаптера; аренда не должна пережидать его.
	OrderCallTimeout time.Duration
	// QuoteTTL — дольше этого цена доставки не держится, что бы ни обещал магазин.
	QuoteTTL time.Duration
	// PaymentStub включает единственную доступную сейчас оплату без движения денег.
	PaymentStub bool
	// ReturnBaseURL — MARKETPLACE_PUBLIC_URL витрины.
	ReturnBaseURL string
	CartTTL       time.Duration
	// QuoteTimeout — сколько ждать ответа магазина на расчёт.
	QuoteTimeout time.Duration
}

// Service — покупка покупателя.
type Service struct {
	observer ports.CheckoutObserver
	carts    ports.CartVerifier
	uow      ports.UnitOfWork
	platform ports.PlatformAPI
	points   ports.PickupLookup
	ids      ports.IDs
	clock    ports.Clock
	config   Config
}

// NewService — сценарии покупки.
func NewService(
	uow ports.UnitOfWork,
	platform ports.PlatformAPI,
	points ports.PickupLookup,
	carts ports.CartVerifier,
	identities ports.IDs,
	clock ports.Clock,
	config Config,
) *Service {
	return &Service{
		carts:    carts,
		uow:      uow,
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
	codes := make(map[ids.TenantID]string)

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

		for tenant, promo := range c.State().Promos {
			if promo.Valid || promo.Message == nil {
				codes[tenant] = promo.Code
			}
		}
		for _, line := range c.Lines() {
			if line.Issue == cart.IssueUnavailable || line.Issue == cart.IssueNotEnoughStock {
				message := "в корзине есть недоступные позиции: " + line.Offer.String()

				return checkout.ErrOfferUnavailable.WithMessage(message)
			}

			lines = append(
				lines,
				checkout.Line{
					Offer:     line.Offer,
					Name:      line.Name,
					Quantity:  line.Quantity,
					Price:     line.Price,
					Discounts: line.Discounts,
				},
			)
		}

		return nil
	})
	if err != nil {
		return checkout.State{}, err
	}

	created, err := checkout.New(s.ids.NewUUID(), buyerID, key, lines, s.clock.Now())
	if err != nil {
		return checkout.State{}, err
	}

	settings, err := s.deliverySettings(ctx, created.Groups())
	if err != nil {
		return checkout.State{}, err
	}
	options := make(map[ids.TenantID][]string, len(settings))
	regions := make(map[ids.TenantID][]string, len(settings))
	for tenant, setting := range settings {
		options[tenant] = setting.Delivery.Carriers
		regions[tenant] = setting.Delivery.Regions
	}
	if err := created.SetDeliveryOptions(options, regions, s.clock.Now()); err != nil {
		return checkout.State{}, err
	}

	if err := s.ready(ctx, created.Groups()); err != nil {
		return checkout.State{}, err
	}

	if _, err := created.SetPromoCodes(codes, s.clock.Now()); err != nil {
		return checkout.State{}, err
	}
	checks, err := s.check(ctx, buyerID, created.Groups())
	if err != nil {
		return checkout.State{}, s.verifyOnPromoError(ctx, buyerID, err)
	}
	changed, err := created.Reprice(checks, s.clock.Now())
	if err != nil {
		if errors.Is(err, checkout.ErrOfferUnavailable) {
			s.verifyCart(ctx, buyerID)
		}
		return checkout.State{}, err
	}
	if changed {
		s.verifyCart(ctx, buyerID)
		return checkout.State{}, checkout.ErrChanged
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

	return s.paymentView(ctx, c)
}

// Quote выбирает пункт выдачи и спрашивает расчёт у каждого продавца
// покупки. Продавец без этого перевозчика получает расчёт «недоступно» с причиной.
func (s *Service) Quote(
	ctx context.Context,
	buyerID, checkoutID ids.UUID,
	carrier delivery.Carrier,
	pointCode, city string,
) (checkout.State, error) {
	return s.selectDelivery(ctx, buyerID, checkoutID, nil, carrier, pointCode, city)
}

// QuoteGroup выбирает ПВЗ одного магазина, не меняя снимки остальных.
func (s *Service) QuoteGroup(
	ctx context.Context,
	buyerID, checkoutID ids.UUID,
	tenant ids.TenantID,
	carrier delivery.Carrier,
	pointCode, city string,
) (checkout.State, error) {
	if tenant == "" {
		return s.selectDelivery(ctx, buyerID, checkoutID, nil, carrier, pointCode, city)
	}
	return s.selectDelivery(ctx, buyerID, checkoutID, []ids.TenantID{tenant}, carrier, pointCode, city)
}

// QuoteGroups рассчитывает общий ПВЗ выбранных магазинов параллельно.
func (s *Service) QuoteGroups(
	ctx context.Context,
	buyerID, checkoutID ids.UUID,
	tenants []ids.TenantID,
	carrier delivery.Carrier,
	pointCode, city string,
) (checkout.State, error) {
	if len(tenants) == 0 {
		return checkout.State{}, checkout.ErrNotFound
	}
	return s.selectDelivery(ctx, buyerID, checkoutID, tenants, carrier, pointCode, city)
}

func (s *Service) selectDelivery(
	ctx context.Context,
	buyerID, checkoutID ids.UUID,
	targets []ids.TenantID,
	carrier delivery.Carrier,
	pointCode, city string,
) (checkout.State, error) {
	if _, err := delivery.NormalizeCity(city); err != nil {
		return checkout.State{}, err
	}
	unique := make(map[ids.TenantID]bool, len(targets))
	for _, tenant := range targets {
		if unique[tenant] {
			return checkout.State{}, checkout.ErrInvalidGroups
		}
		unique[tenant] = true
	}
	c, err := s.load(ctx, buyerID, checkoutID)
	if err != nil {
		return checkout.State{}, err
	}
	if c.Status() != checkout.StatusDraft {
		return checkout.State{}, checkout.ErrNotDraft
	}

	groups := c.Groups()
	if targets != nil {
		groups = nil
		for _, group := range c.Groups() {
			if unique[group.TenantID] {
				groups = append(groups, group)
			}
		}
		if len(groups) != len(unique) {
			return checkout.State{}, checkout.ErrNotFound
		}
	}

	for _, g := range groups {
		if g.Status != "" {
			selected := g.Selection(c.Delivery())
			if g.Status != checkout.GroupDeliveryRequoted || selected == nil || selected.Carrier != carrier ||
				selected.PickupPointCode != pointCode || !strings.EqualFold(selected.City, strings.TrimSpace(city)) {
				return c.State(), checkout.ErrSnapshotChanged
			}
		}
	}
	point, err := s.points.Point(ctx, carrier, pointCode, city)
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

	quotes := s.quote(ctx, buyerID, groups, selection)

	choose := func(c *checkout.Checkout, now time.Time) error {
		if len(groups) == 1 && groups[0].Status == checkout.GroupDeliveryRequoted {
			if selected := groups[0].Selection(c.Delivery()); selected == nil || *selected != selection {
				return checkout.ErrSnapshotChanged
			}
			return c.ApplyDispatchQuote(groups[0].TenantID, quotes[groups[0].TenantID], now)
		}
		if targets != nil {
			return c.SelectDeliveries(selection, quotes, now)
		}
		return c.SelectDelivery(selection, quotes, now)
	}
	after := func(ctx context.Context, tx ports.Tx, c *checkout.Checkout) error {
		if err := saveBuyerPickup(ctx, tx, c); err != nil {
			return err
		}
		if err := s.enqueueCancellations(ctx, tx, c); err != nil {
			return err
		}
		return s.returnItems(ctx, tx, c)
	}
	return s.retrySelection(ctx, buyerID, checkoutID, after, choose)
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
	available := false
	for _, g := range c.Groups() {
		if g.Quote != nil && g.Quote.Status == delivery.QuoteAvailable {
			available = true
		}
	}
	if !available {
		return c.State(), checkout.ErrDeliveryUnavailable
	}

	reviewed, reviewChanged, err := s.reviewReadiness(ctx, c)
	if err != nil {
		return reviewed, err
	}
	if reviewChanged {
		return reviewed, checkout.ErrDeliveryChanged
	}
	checks, err := s.check(ctx, buyerID, c.Groups())
	if err != nil {
		return checkout.State{}, s.verifyOnPromoError(ctx, buyerID, err)
	}

	var changed bool
	expected := c.State().Version

	repriced, err := s.update(ctx, buyerID, checkoutID, func(c *checkout.Checkout, now time.Time) error {
		if c.State().Version != expected {
			return checkout.ErrConflict
		}
		var err error
		changed, err = c.Reprice(checks, now)

		return err
	})
	if err != nil {
		if errors.Is(err, checkout.ErrOfferUnavailable) {
			s.verifyCart(ctx, buyerID)
		}
		return checkout.State{}, err
	}
	if changed {
		s.verifyCart(ctx, buyerID)
		return repriced, checkout.ErrChanged
	}

	c = checkout.Restore(repriced)
	expected = repriced.Version
	stale := map[ids.TenantID]bool{}
	for _, tenant := range c.NeedsQuote(s.clock.Now()) {
		stale[tenant] = true
	}

	var requoted map[ids.TenantID]delivery.Quote

	if len(stale) > 0 {
		var groups []checkout.Group
		for _, group := range c.Groups() {
			if stale[group.TenantID] && (group.Quote == nil || group.Quote.Status == delivery.QuoteAvailable) {
				groups = append(groups, group)
			}
		}

		requoted = s.groupQuotes(ctx, buyerID, groups, c.Delivery())
	}

	var confirmErr error
	deliveryChanged := false
	for _, group := range c.Groups() {
		if q, found := requoted[group.TenantID]; found && !group.Excluded() {
			if q.Reason == delivery.ReasonSellerUnreachable {
				return c.State(), checkout.ErrSellerUnavailable
			}
			deliveryChanged = deliveryChanged || group.DeliveryChanged(q)
		}
	}

	state, err := s.update(ctx, buyerID, checkoutID, func(c *checkout.Checkout, now time.Time) error {
		if c.State().Version != expected {
			return checkout.ErrConflict
		}
		if len(requoted) > 0 {
			if err := c.Requote(requoted, now); err != nil {
				return err
			}
		}

		// расчёты сохраняются, даже если подтверждение не прошло: покупатель увидит причину
		if deliveryChanged {
			confirmErr = checkout.ErrDeliveryChanged
		} else {
			confirmErr = c.Confirm(now)
		}

		return nil
	},
	)
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
) map[ids.TenantID]delivery.Quote {
	fresh := slices.Clone(groups)
	for i := range fresh {
		if !fresh[i].Abandoned && fresh[i].OrderID == "" {
			fresh[i].Quote = nil
		}
	}
	return s.quoteSelections(ctx, buyerID, fresh, func(checkout.Group) *checkout.Selection { return &selection })
}

func (s *Service) quoteSelections(
	ctx context.Context,
	buyerID ids.UUID,
	groups []checkout.Group,
	selection func(checkout.Group) *checkout.Selection,
) map[ids.TenantID]delivery.Quote {
	var mu sync.Mutex
	var wg sync.WaitGroup
	quotes := make(map[ids.TenantID]delivery.Quote, len(groups))
	for _, group := range groups {
		if group.Excluded() || group.OrderID != "" {
			if group.Quote != nil {
				mu.Lock()
				quotes[group.TenantID] = *group.Quote
				mu.Unlock()
			}
			continue
		}
		selected := selection(group)
		if selected == nil {
			mu.Lock()
			quotes[group.TenantID] = delivery.Quote{
				Status:    delivery.QuoteUnavailable,
				Reason:    delivery.ReasonCarrierNotConnected,
				ExpiresAt: s.clock.Now().Add(s.config.QuoteTTL),
			}
			mu.Unlock()
			continue
		}
		wg.Add(1)
		go func(group checkout.Group, selected checkout.Selection) {
			defer wg.Done()
			q := s.quoteGroup(ctx, buyerID, group, selected)
			mu.Lock()
			quotes[group.TenantID] = q
			mu.Unlock()
		}(group, *selected)
	}
	wg.Wait()
	return quotes
}

func (s *Service) quoteGroup(
	ctx context.Context,
	buyerID ids.UUID,
	group checkout.Group,
	selection checkout.Selection,
) delivery.Quote {
	if group.Carriers != nil && (!slices.Contains(group.Carriers, string(selection.Carrier)) ||
		!slices.Contains(group.Regions, selection.RegionCode)) {
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

	var review *ports.DispatchReview
	if group.Status == checkout.GroupDeliveryRequoted && group.DispatchReviewRevision > 0 {
		review = &ports.DispatchReview{
			ExternalCheckoutID: group.ExternalCheckoutID,
			GroupID:            group.TenantID,
			Revision:           group.DispatchReviewRevision,
		}
	}
	offered, err := s.platform.QuoteDelivery(callCtx, group.TenantID, ports.QuoteRequest{
		DispatchReview:  review,
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
		return cost, nil
	}

	quote, err := delivery.NewQuote(offered, apply, now, s.config.QuoteTTL)
	if err != nil {
		return delivery.Unreachable(now)
	}

	return quote
}

// deliverySettings читает службы и регионы магазинов одним запросом проекции.
func (s *Service) deliverySettings(
	ctx context.Context,
	groups []checkout.Group,
) (map[ids.TenantID]participant.State, error) {
	settings := make(map[ids.TenantID]participant.State, len(groups))
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		keys := make([]ids.TenantID, len(groups))
		for i, group := range groups {
			keys[i] = group.TenantID
		}
		participants, err := tx.Participants().Batch(ctx, keys)
		if err != nil {
			return err
		}
		for tenant, p := range participants {
			settings[tenant] = p.State()
		}

		return nil
	})

	return settings, err
}

// check спрашивает у магазинов цену и наличие позиций покупки.
func (s *Service) check(
	ctx context.Context,
	buyerID ids.UUID,
	groups []checkout.Group,
) (map[ids.OfferID]checkout.PriceCheck, error) {
	cartGroups := make([]cart.Group, 0, len(groups))
	checks := make(map[ids.OfferID]checkout.PriceCheck)

	for _, group := range groups {
		if group.Excluded() || group.Status != "" {
			for _, line := range group.Lines {
				checks[line.Offer] = checkout.PriceCheck{
					Exists:    true,
					Price:     line.Price,
					Available: line.Quantity,
					Discounts: line.Discounts,
				}
			}
			continue
		}

		cartGroup := cart.Group{TenantID: group.TenantID}
		if group.PromoCode != "" {
			cartGroup.PromoCode = &cart.PromoCode{Code: group.PromoCode}
		}
		for _, line := range group.Lines {
			cartGroup.Lines = append(
				cartGroup.Lines,
				cart.Line{Offer: line.Offer, Quantity: line.Quantity, Price: line.Price},
			)
		}

		cartGroups = append(cartGroups, cartGroup)
	}

	results, err := appcart.PriceGroups(ctx, s.platform, buyerID, cartGroups)
	if err != nil {
		return nil, err
	}
	for tenant, result := range results {
		if result.PromoCode != nil && !result.PromoCode.Valid {
			if result.PromoCode.Message != nil {
				return nil, checkout.ErrPromoInvalid.WithMessage(*result.PromoCode.Message)
			}
			return nil, checkout.ErrPromoInvalid
		}
		for _, item := range result.Items {
			checks[ids.OfferID{Tenant: tenant, Product: item.ProductID}] = checkout.PriceCheck{
				Exists:    item.Exists,
				Price:     item.Price,
				Available: item.Available,
				Discounts: result.Discounts[item.ProductID],
			}
		}
	}

	return checks, nil
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
	var err error
	for range 3 {
		state, err = s.updateSelection(ctx, buyerID, checkoutID, nil, mutate)
		if !errors.Is(err, checkout.ErrConflict) {
			break
		}
	}
	return state, err
}

func (s *Service) updateSelection(
	ctx context.Context,
	buyerID, checkoutID ids.UUID,
	after func(context.Context, ports.Tx, *checkout.Checkout) error,
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
		previous := c.State()
		before := previous.Version
		if err := mutate(c, s.clock.Now()); err != nil {
			return err
		}

		if after != nil {
			if err := after(ctx, tx, c); err != nil {
				return err
			}
		}
		if c.State().Version != before {
			if err := tx.Checkouts().Update(ctx, c); err != nil {
				return err
			}
		}

		state = c.State()
		s.observeTransitions(tx, previous, state)

		return nil
	})

	return state, err
}

func (s *Service) verifyOnPromoError(ctx context.Context, buyerID ids.UUID, cause error) error {
	if errors.Is(cause, checkout.ErrPromoInvalid) {
		s.verifyCart(ctx, buyerID)
	}
	return cause
}

// Перепроверка корзины вторична: её сбой не отменяет результат операции покупки.
func (s *Service) verifyCart(ctx context.Context, buyerID ids.UUID) {
	if _, err := s.carts.Verify(ctx, buyerID); err != nil {
		slog.Warn("не удалось перепроверить корзину", "buyerId", string(buyerID), "error", err)
	}
}

func saveBuyerPickup(ctx context.Context, tx ports.Tx, c *checkout.Checkout) error {
	if selection := c.Delivery(); selection != nil {
		repo := tx.BuyerFeatures()
		if err := repo.Lock(ctx, c.BuyerID()); err != nil {
			return err
		}
		profile, err := repo.Profile(ctx, c.BuyerID())
		if err != nil {
			return err
		}
		changed, err := profile.SelectPickup(buyer.Pickup{
			Provider:   string(selection.Carrier),
			Code:       selection.PickupPointCode,
			City:       selection.City,
			Address:    selection.Address,
			RegionCode: selection.RegionCode,
		})
		if err != nil {
			return err
		}
		if changed {
			if err := repo.SaveProfile(ctx, c.BuyerID(), profile); err != nil {
				return err
			}
		}
	}
	return nil
}

// retrySelection не отдаёт покупателю конфликт с независимой группой той же покупки.
func (s *Service) retrySelection(
	ctx context.Context,
	buyerID, id ids.UUID,
	after func(context.Context, ports.Tx, *checkout.Checkout) error,
	mutate func(*checkout.Checkout, time.Time) error,
) (checkout.State, error) {
	for {
		state, err := s.updateSelection(ctx, buyerID, id, after, mutate)
		if !errors.Is(err, checkout.ErrConflict) && !errors.Is(err, cart.ErrConflict) {
			return state, err
		}
		if err := waitUpdate(ctx); err != nil {
			return checkout.State{}, err
		}
	}
}

func waitUpdate(ctx context.Context) error {
	timer := time.NewTimer(updateRetryDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// SetObserver вызывается при сборке до запуска обработчиков.
func (s *Service) SetObserver(observer ports.CheckoutObserver) { s.observer = observer }

func (s *Service) observeTransitions(tx ports.Tx, before, after checkout.State) {
	if s.observer == nil {
		return
	}
	for i, g := range after.Groups {
		old := before.Groups[i]
		events := make([]string, 0, 6)
		if g.CancellationConfirmed && !old.CancellationConfirmed {
			events = append(events, "cancellation_confirmed")
		}
		if g.CancellationBlocked && !old.CancellationBlocked {
			events = append(events, "cancellation_refused")
		}
		if g.Status == checkout.GroupUnconfirmed && old.Status != g.Status {
			events = append(events, "creation_expired")
		}
		if g.ReconciliationRequired && !old.ReconciliationRequired {
			events = append(events, "reconciliation_required")
		}
		if g.Abandoned && g.OrderID != "" && old.OrderID == "" {
			events = append(events, "late_order")
		}
		if g.Reason == "buyer_cancelled" && old.Reason != g.Reason {
			events = append(events, "buyer_cancelled")
		}
		for _, event := range events {
			tx.AfterCommit(func() { s.observer.RecordCheckoutEvent(g.TenantID, after.ID, event) })
		}
	}
}
