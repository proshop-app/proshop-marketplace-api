package promotion_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/promotion"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

type chooseLast struct{}

func (chooseLast) IntN(n int) int { return n - 1 }

func campaign(t *testing.T) *promotion.Campaign {
	t.Helper()
	c, err := promotion.NewCampaign(
		promotion.CampaignState{
			ID:            "00000000-0000-0000-0000-000000000001",
			TenantID:      "t1",
			Kind:          promotion.SponsoredSlot,
			Marking:       promotion.Marking{Advertiser: "Рекламодатель", INN: "1234567890"},
			PlacementCode: "category_slot",
			Position:      1,
			Target:        promotion.Target{OfferID: ids.OfferID{Tenant: "t1", Product: "p1"}, CategoryID: "c1"},
			Period:        promotion.Period{From: "2026-11-09", To: "2026-11-15"},
			Price:         money.FromKopecks(700),
		},
		time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC),
		time.UTC,
	)
	if err != nil {
		t.Fatal(err)
	}
	c.MarkSaved()
	return c
}

func TestLifecycle(t *testing.T) {
	c := campaign(t)
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	changed, err := c.Decide(promotion.Submit, "", "", promotion.Payment{}, now)
	if err != nil || !changed {
		t.Fatal(changed, err)
	}
	c.MarkSaved()
	changed, err = c.Decide(promotion.Submit, "", "", promotion.Payment{}, now)
	if err != nil || changed {
		t.Fatal(changed, err)
	}
	_, err = c.Decide(promotion.Approve, "", "", promotion.Payment{}, now)
	if !errors.Is(err, promotion.ErrERIDRequired) {
		t.Fatal(err)
	}
	if _, err := c.Decide(promotion.Approve, "", "erid", promotion.Payment{}, now); err != nil {
		t.Fatal(err)
	}
	c.MarkSaved()
	if c.Tick(now, time.UTC) {
		t.Fatal("без оплаты не активируется")
	}
	payment := promotion.Payment{Amount: money.FromKopecks(700), PaidOn: "2026-11-09"}
	if _, err := c.Decide(promotion.Pay, "", "", payment, now); err != nil {
		t.Fatal(err)
	}
	c.MarkSaved()
	if c.Tick(now.Add(-time.Second), time.UTC) {
		t.Fatal("слишком ранняя активация")
	}
	if !c.Tick(now, time.UTC) || c.State().Status != promotion.Active {
		t.Fatal(c.State())
	}
	if c.Tick(now, time.UTC) {
		t.Fatal("повтор не должен менять версию")
	}
	c.MarkSaved()
	if c.Tick(now.AddDate(0, 0, 6), time.UTC) {
		t.Fatal("воскресенье ещё оплачено")
	}
	if !c.Tick(now.AddDate(0, 0, 7), time.UTC) || c.State().Status != promotion.Finished {
		t.Fatal(c.State())
	}
}

func TestTerminalAndInvalidActions(t *testing.T) {
	for _, status := range []promotion.Status{promotion.Review, promotion.Approved, promotion.Active} {
		s := campaign(t).State()
		s.Status = status
		c := promotion.RestoreCampaign(s)
		changed, err := c.Decide(promotion.Stop, "", "", promotion.Payment{}, time.Now())
		if !changed || err != nil {
			t.Fatal(changed, err)
		}
		c.MarkSaved()
		for _, action := range []promotion.Action{
			promotion.Submit,
			promotion.Approve,
			promotion.Pay,
			promotion.Finish,
		} {
			_, err := c.Decide(action, "", "erid", promotion.Payment{}, time.Now())
			if !errors.Is(err, promotion.ErrConflict) {
				t.Fatal(action, err)
			}
		}
		if changed, err := c.Decide(promotion.Stop, "", "", promotion.Payment{}, time.Now()); changed || err != nil {
			t.Fatal(changed, err)
		}
	}
	c := campaign(t)
	_, err := c.Decide("pause", "", "", promotion.Payment{}, time.Now())
	if !errors.Is(err, promotion.ErrActionUnsupported) {
		t.Fatal(err)
	}
	for _, kind := range []promotion.Kind{promotion.Banner, promotion.Showcase, "boost"} {
		s := c.State()
		s.Kind = kind
		if _, err := promotion.NewCampaign(s, time.Now(), time.UTC); !errors.Is(err, promotion.ErrKindUnsupported) {
			t.Fatal(err)
		}
	}
}

func TestWeeksAcrossDSTAndCapacity(t *testing.T) {
	location, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	period, err := promotion.NewPeriod("2026-03-23", 2, location)
	if err != nil || period.To != "2026-04-05" {
		t.Fatal(period, err)
	}
	weeks, err := period.Weeks(location)
	if err != nil || len(weeks) != 2 || weeks[1] != "2026-03-30" {
		t.Fatal(weeks, err)
	}
	for _, input := range []string{"2026-03-24", "bad"} {
		if _, err := promotion.NewPeriod(input, 1, location); !errors.Is(err, promotion.ErrInvalid) {
			t.Fatal(err)
		}
	}
	err = promotion.CheckCapacity([]promotion.Week{{From: weeks[0], Sold: 1}, {From: weeks[1], Sold: 2}}, 2)
	var conflict *promotion.CapacityError
	if !errors.As(err, &conflict) || len(conflict.Weeks) != 1 || conflict.Weeks[0] != weeks[1] {
		t.Fatal(err)
	}
	if err := promotion.CheckCapacity([]promotion.Week{{From: weeks[0], Sold: 1}}, 2); err != nil {
		t.Fatal(err)
	}
}

func TestPlacementAndRotation(t *testing.T) {
	p := promotion.RestorePlacement(promotion.PlacementState{
		Code:      "category_slot",
		Positions: []int{1, 5},
		SlotLimit: 1,
		Version:   1,
	})
	if _, err := p.Configure(1, money.Money{}, nil, true); err == nil {
		t.Fatal("нулевая цена")
	}
	prices := map[int]money.Money{5: money.FromKopecks(200)}
	if _, err := p.Configure(2, money.FromKopecks(100), prices, true); err != nil {
		t.Fatal(err)
	}
	prices[5] = money.Money{}
	price, err := p.Quote(5, 3)
	if err != nil || price.Kopecks() != 600 {
		t.Fatal(price, err)
	}
	snapshot := p.State()
	snapshot.PositionPrices[5] = money.Money{}
	if p.Price(5).Kopecks() != 200 {
		t.Fatal("снимок меняет агрегат")
	}
	a := campaign(t).State()
	a.Status = promotion.Active
	b := a
	b.ID = "00000000-0000-0000-0000-000000000002"
	c := a
	c.Position = 5
	d := a
	d.Status = promotion.Draft
	result := promotion.Rotate([]promotion.CampaignState{a, b, c, d}, chooseLast{})
	if len(result) != 2 || result[0].ID != b.ID || result[1].Position != 5 {
		t.Fatal(result)
	}
}

func TestMarkingAndRevise(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	for _, inn := range []string{"1234567890", "123456789012"} {
		if err := (promotion.Marking{Advertiser: "Рекламодатель", INN: inn}).Validate(true); err != nil {
			t.Fatal(err)
		}
	}
	for _, inn := range []string{"123", "123456789a", "１２３４５６７８９０", " 1234567890"} {
		if err := (promotion.Marking{INN: inn}).Validate(false); !errors.Is(err, promotion.ErrInvalid) {
			t.Fatal(inn, err)
		}
	}
	for _, marking := range []promotion.Marking{{}, {Advertiser: "Имя"}, {INN: "1234567890"}} {
		s := campaign(t).State()
		s.Marking = marking
		c, err := promotion.NewCampaign(s, now, time.UTC)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Decide(promotion.Submit, "", "", promotion.Payment{}, now); !errors.Is(
			err,
			promotion.ErrMarkingRequired,
		) {
			t.Fatal(err)
		}
	}
	c := campaign(t)
	for _, action := range []promotion.Action{promotion.Submit, promotion.Reject, promotion.Revise} {
		before := c.State().Version
		if changed, err := c.Decide(action, "Причина", "", promotion.Payment{}, now); !changed || err != nil {
			t.Fatal(action, changed, err)
		}
		if c.State().Version != before+1 {
			t.Fatal(c.State())
		}
		c.MarkSaved()
	}
	if c.State().Status != promotion.Draft || c.State().RejectionReason != "" {
		t.Fatal(c.State())
	}
	if changed, err := c.Decide(promotion.Revise, "", "", promotion.Payment{}, now); changed || err != nil {
		t.Fatal(changed, err)
	}
}

func TestRefund(t *testing.T) {
	for _, status := range []promotion.Status{
		promotion.Draft,
		promotion.Review,
		promotion.Approved,
		promotion.Rejected,
		promotion.Active,
		promotion.Stopped,
		promotion.Finished,
	} {
		s := campaign(t).State()
		s.Status = status
		s.Payment.Amount = money.FromKopecks(700)
		for _, amount := range []int64{-1, 0, 701, 700, 300} {
			c := promotion.RestoreCampaign(s)
			payment := promotion.Payment{Refund: promotion.RefundRecord{
				Amount:   money.FromKopecks(amount),
				On:       "2026-11-06",
				Document: "Документ",
				Note:     "Решение оператора",
			}}
			changed, err := c.Decide(promotion.Refund, "", "", payment, time.Now())
			valid := (status == promotion.Stopped || status == promotion.Finished) && amount > 0 && amount <= 700
			if valid != changed || valid != (err == nil) {
				t.Fatal(status, amount, changed, err)
			}
			if !valid {
				if c.State() != s {
					t.Fatal("ошибка изменила агрегат")
				}
				continue
			}
			c.MarkSaved()
			if changed, err := c.Decide(promotion.Refund, "", "", payment, time.Now()); changed || err != nil {
				t.Fatal(changed, err)
			}
			payment.Refund.On = "bad"
			_, err = c.Decide(promotion.Refund, "", "", payment, time.Now())
			if !errors.Is(err, promotion.ErrInvalid) {
				t.Fatal(err)
			}
		}
	}
}

func TestRefundStateJSON(t *testing.T) {
	s := campaign(t).State()
	s.Status = promotion.Finished
	s.Payment = promotion.Payment{
		Status: promotion.Paid,
		Amount: money.FromKopecks(700),
		Refund: promotion.RefundRecord{
			Amount:   money.FromKopecks(300),
			On:       "2026-11-06",
			Document: "Документ",
			Note:     "Возврат",
		},
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var restored promotion.CampaignState
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Payment != s.Payment {
		t.Fatal(restored.Payment)
	}
}

func TestPayCannotRecordRefund(t *testing.T) {
	s := campaign(t).State()
	s.Status = promotion.Review
	c := promotion.RestoreCampaign(s)
	payment := promotion.Payment{
		Amount: money.FromKopecks(700),
		PaidOn: "2026-11-06",
		Refund: promotion.RefundRecord{Amount: money.FromKopecks(300), On: "2026-11-06"},
	}
	_, err := c.Decide(promotion.Pay, "", "", payment, time.Now())
	if !errors.Is(err, promotion.ErrInvalid) || c.State() != s {
		t.Fatal(c.State(), err)
	}
}
