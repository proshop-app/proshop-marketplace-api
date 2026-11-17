package promotion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/promotion"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const promotionTickBatch = 200

// Sponsored содержит выбранную карточку и обязательную маркировку.
type Sponsored struct {
	Position    int
	PromotionID ids.UUID
	ERID        string
	Offer       ports.OfferDocument
}

// Event передаёт показ или клик без влияния на органическое ранжирование.
type Event struct {
	PromotionID ids.UUID `json:"promotionId"`
	Kind        string   `json:"kind"`
}

// Sponsored содержит выбранную карточку и обязательную маркировку.
func (s *Service) Sponsored(ctx context.Context, query ports.OfferQuery) ([]Sponsored, error) {
	// Рекламный подбор не является отправкой пользовательского поиска.
	query.TrackQuery = false
	result := []Sponsored{}
	if query.Offset != 0 || (query.Text == "" && query.CategoryID == "") {
		return result, nil
	}
	if s.config.Catalog != nil {
		normalized, err := s.config.Catalog.PrepareQuery(ctx, query)
		if err != nil {
			return nil, err
		}
		query = normalized
	}
	code := "category_slot"
	relevant := map[string]ports.OfferDocument{}
	if query.Text != "" {
		code = "search_slot"
		query.Offset, query.Limit = 0, s.config.SearchDepth
		// Рекламному подбору нужны только карточки: фасеты и гистограмма не читаются.
		facets := []ports.FacetCode{}
		query.Facets = &facets
		query.FacetFields = []taxonomy.Attribute{}
		page, err := s.index.Search(ctx, query)
		if err != nil {
			return nil, err
		}
		for _, doc := range page.Items {
			relevant[doc.ID] = doc
		}
	}
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		day := s.clock.Now().In(s.config.Location).Format(time.DateOnly)
		filter := promotion.Filter{Status: promotion.Active, PlacementCode: code, From: day, To: day}
		selection := promotion.Selection{Filter: filter, CategoryID: query.CategoryID}
		if code == "search_slot" {
			selection.CategoryID = ""
			selection.Offers = make([]ids.OfferID, 0, len(relevant))
			for id := range relevant {
				offer, err := ids.ParseOfferID(id)
				if err != nil {
					return err
				}
				selection.Offers = append(selection.Offers, offer)
			}
		}
		all, err := tx.Promotion().Select(ctx, selection)
		if err != nil {
			return err
		}
		states, err := campaignOffers(ctx, tx, all)
		if err != nil {
			return err
		}
		candidates := []promotion.CampaignState{}
		docs := map[ids.UUID]ports.OfferDocument{}
		for _, c := range all {
			if code == "category_slot" && c.Target.CategoryID != query.CategoryID {
				continue
			}
			if _, ok := relevant[c.Target.OfferID.String()]; code == "search_slot" && !ok {
				continue
			}
			doc, ok := campaignOffer(states[c.Target.OfferID], c)
			if !ok {
				continue
			}
			if code == "search_slot" {
				priced := relevant[c.Target.OfferID.String()]
				doc.Price, doc.BasePrice = priced.Price, priced.BasePrice
			} else if query.PriceContexts != nil {
				profile, ok := catalog.SelectPrice(doc.PriceProfiles, query.PriceContexts[c.TenantID])
				if !ok {
					continue
				}
				doc.Price = profile.EffectivePrice.Kopecks()
				doc.BasePrice = profile.Price.Kopecks()
			}
			candidates = append(candidates, c)
			docs[c.ID] = doc
		}
		for _, c := range promotion.Rotate(candidates, s.config.Random) {
			result = append(result, Sponsored{
				Position:    c.Position,
				PromotionID: c.ID,
				ERID:        c.Marking.ERID,
				Offer:       docs[c.ID],
			})
		}
		return nil
	})
	return result, err
}

func currentOffer(ctx context.Context, tx ports.Tx, c promotion.CampaignState) (ports.OfferDocument, bool, error) {
	state, err := tx.Offers().Offer(ctx, c.Target.OfferID.Tenant, c.Target.OfferID.Product)
	if errors.Is(err, catalog.ErrProductNotFound) {
		return ports.OfferDocument{}, false, nil
	}
	if err != nil {
		return ports.OfferDocument{}, false, err
	}
	doc, listed := campaignOffer(state, c)
	return doc, listed, nil
}

func campaignOffer(state ports.OfferState, c promotion.CampaignState) (ports.OfferDocument, bool) {
	if state.Product == nil {
		return ports.OfferDocument{}, false
	}
	doc, ok := appcatalog.Document(state)
	card := state.Product.State().Card.Marketplace
	if card == nil {
		return doc, false
	}
	if card.CategoryID != c.Target.CategoryID {
		// Такой документ не выдаётся: категория нужна только для пояснения остановки.
		doc.CategoryID = card.CategoryID
		return doc, false
	}
	return doc, ok && doc.Listed && doc.CategoryID == c.Target.CategoryID
}

// Events обезличивает посетителя и атомарно учитывает пачку маячков.
func (s *Service) Events(ctx context.Context, visitor string, events []Event) error {
	if visitor == "" || len(visitor) > 1024 || len(events) > 50 {
		return promotion.ErrInvalid
	}
	for _, event := range events {
		if event.Kind != "view" && event.Kind != "click" {
			return promotion.ErrInvalid
		}
	}
	hash := sha256.Sum256([]byte(s.config.VisitorSalt + "\x00" + visitor))
	visitorHash := hex.EncodeToString(hash[:])
	return s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		day := s.clock.Now().In(s.config.Location).Format(time.DateOnly)
		for _, event := range events {
			if _, err := ids.ParseUUID(string(event.PromotionID)); err != nil {
				continue
			}
			c, err := tx.Promotion().Get(ctx, event.PromotionID)
			if errors.Is(err, promotion.ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			state := c.State()
			if state.Status != promotion.Active || day < state.Period.From || day > state.Period.To {
				continue
			}
			changed, err := tx.Promotion().Count(ctx, state, day, visitorHash, event.Kind)
			if err != nil {
				return err
			}
			if changed && s.config.Metrics != nil {
				tx.AfterCommit(func() { s.config.Metrics.PromotionEvent(state, event.Kind) })
			}
		}
		return nil
	})
}

// Tick выбирает только живые размещения; просроченная очередь освобождает резерв.
func (s *Service) Tick(ctx context.Context) error {
	day := s.clock.Now().In(s.config.Location).Format(time.DateOnly)
	for _, status := range []promotion.Status{promotion.Active, promotion.Approved, promotion.Review} {
		selection := promotion.Selection{
			Filter: promotion.Filter{Status: status},
			Limit:  promotionTickBatch,
			ByID:   true,
		}
		if status == promotion.Review {
			selection.EndBefore = day
		}
		for {
			next, err := s.tickPage(ctx, selection)
			if err != nil {
				return err
			}
			if next == "" {
				break
			}
			selection.After = next
		}
	}
	return nil
}

func (s *Service) tickPage(ctx context.Context, selection promotion.Selection) (ids.UUID, error) {
	var next ids.UUID
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		all, err := tx.Promotion().Select(ctx, selection)
		if err != nil {
			return err
		}
		current := make([]promotion.CampaignState, 0, len(all))
		day := s.clock.Now().In(s.config.Location).Format(time.DateOnly)
		for _, state := range all {
			if state.Period.To >= day {
				current = append(current, state)
			}
		}
		offers, err := campaignOffers(ctx, tx, current)
		if err != nil {
			return err
		}
		for _, state := range all {
			if err := s.tickCampaign(ctx, tx, state, offers[state.Target.OfferID]); err != nil {
				return err
			}
		}
		if len(all) == selection.Limit {
			next = all[len(all)-1].ID
		}
		return nil
	})
	return next, err
}

func (s *Service) tickCampaign(
	ctx context.Context,
	tx ports.Tx,
	state promotion.CampaignState,
	offer ports.OfferState,
) error {
	now := s.clock.Now().In(s.config.Location)
	expired := now.Format(time.DateOnly) > state.Period.To
	campaign := promotion.RestoreCampaign(state)
	action, note := promotion.Activate, ""
	var changed bool
	var err error
	switch {
	case expired && state.Status != promotion.Active:
		action, note = promotion.Stop, "период прошёл без оплаты/решения"
		changed, err = campaign.Decide(action, note, "", promotion.Payment{}, now)
	case expired:
		action = promotion.Finish
		changed = campaign.Tick(now, s.config.Location)
	default:
		doc, listed := campaignOffer(offer, state)
		if listed {
			changed = campaign.Tick(now, s.config.Location)
		} else {
			action, note = promotion.Stop, "товар больше не опубликован"
			if doc.CategoryID != "" && doc.CategoryID != state.Target.CategoryID {
				note = "категория товара изменилась"
			}
			changed, err = campaign.Decide(action, note, "", promotion.Payment{}, now)
		}
	}
	if err != nil || !changed {
		return err
	}
	return s.saveDecision(ctx, tx, campaign, state.Status, promotion.ActorSystem, action, note)
}

func campaignOffers(
	ctx context.Context,
	tx ports.Tx,
	campaigns []promotion.CampaignState,
) (map[ids.OfferID]ports.OfferState, error) {
	keys := make([]ids.OfferID, 0, len(campaigns))
	seen := map[ids.OfferID]bool{}
	for _, c := range campaigns {
		if !seen[c.Target.OfferID] {
			keys = append(keys, c.Target.OfferID)
			seen[c.Target.OfferID] = true
		}
	}
	result := map[ids.OfferID]ports.OfferState{}
	if len(keys) == 0 {
		return result, nil
	}
	states, err := tx.Offers().Batch(ctx, keys)
	for _, state := range states {
		p := state.Product.State()
		result[ids.OfferID{Tenant: p.TenantID, Product: p.ProductID}] = state
	}
	return result, err
}
