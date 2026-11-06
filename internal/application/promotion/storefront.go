package promotion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/promotion"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

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
		page, err := s.index.Search(ctx, query)
		if err != nil {
			return nil, err
		}
		for _, doc := range page.Items {
			relevant[doc.ID] = doc
		}
	}
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		day := s.clock.Now().In(s.config.Location).Format("2006-01-02")
		filter := promotion.Filter{Status: promotion.Active, PlacementCode: code, From: day, To: day}
		all, _, err := tx.Promotion().List(ctx, filter)
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
			doc, ok, err := currentOffer(ctx, tx, c)
			if err != nil {
				return err
			}
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
	doc, ok := appcatalog.Document(state)
	return doc, ok && doc.Listed, nil
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
		day := s.clock.Now().In(s.config.Location).Format("2006-01-02")
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

// Tick вызывается под общей блокировкой фонового цикла.
func (s *Service) Tick(ctx context.Context) error {
	return s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		all, _, err := tx.Promotion().List(ctx, promotion.Filter{})
		if err != nil {
			return err
		}
		for _, state := range all {
			if state.Status != promotion.Active && state.Status != promotion.Approved {
				continue
			}
			c := promotion.RestoreCampaign(state)
			_, listed, err := currentOffer(ctx, tx, state)
			if err != nil {
				return err
			}
			action := promotion.Action("activate")
			note := ""
			changed := false
			day := s.clock.Now().In(s.config.Location).Format("2006-01-02")
			due := state.Status == promotion.Approved && state.Payment.Status == promotion.Paid &&
				day >= state.Period.From
			if !listed && (state.Status == promotion.Active || due) {
				action, note = promotion.Stop, "товар больше не опубликован"
				changed, err = c.Decide(action, note, "", promotion.Payment{}, s.clock.Now())
			} else {
				changed = c.Tick(s.clock.Now(), s.config.Location)
				if c.State().Status == promotion.Finished {
					action = promotion.Finish
				}
			}
			if err != nil {
				return err
			}
			if changed {
				if err := s.saveDecision(ctx, tx, c, state.Status, "system", action, note); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
