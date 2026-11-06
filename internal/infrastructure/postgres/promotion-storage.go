package postgres

import (
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/promotion"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// storedCampaignState фиксирует формат jsonb независимо от домена.
type storedCampaignState struct {
	ID              ids.UUID         `json:"ID"`
	TenantID        ids.TenantID     `json:"TenantID"`
	Kind            promotion.Kind   `json:"Kind"`
	PlacementCode   string           `json:"PlacementCode"`
	Position        int              `json:"Position"`
	Target          storedTarget     `json:"Target"`
	Period          storedPeriod     `json:"Period"`
	Price           money.Money      `json:"Price"`
	Marking         storedMarking    `json:"Marking"`
	Payment         storedPayment    `json:"Payment"`
	Status          promotion.Status `json:"Status"`
	Version         int64            `json:"Version"`
	CreatedAt       time.Time        `json:"CreatedAt"`
	UpdatedAt       time.Time        `json:"UpdatedAt"`
	Title           string           `json:"Title"`
	RejectionReason string           `json:"RejectionReason"`
}

// storedTarget фиксирует формат jsonb независимо от домена.
type storedTarget struct {
	OfferID    storedOfferID `json:"OfferID"`
	CategoryID string        `json:"CategoryID"`
	SaleID     string        `json:"SaleID"`
	StoreID    string        `json:"StoreID"`
}

// storedPayment фиксирует формат jsonb независимо от домена.
type storedPayment struct {
	Refund   storedRefundRecord      `json:"refund"`
	Status   promotion.PaymentStatus `json:"Status"`
	PaidOn   string                  `json:"PaidOn"`
	Amount   money.Money             `json:"Amount"`
	Document string                  `json:"Document"`
	Note     string                  `json:"Note"`
}

// storedMarking фиксирует формат jsonb независимо от домена.
type storedMarking struct {
	Advertiser string `json:"advertiser"`
	INN        string `json:"inn,omitempty"`
	ERID       string `json:"erid,omitempty"`
}

// storedRefundRecord фиксирует формат jsonb независимо от домена.
type storedRefundRecord struct {
	Amount   money.Money `json:"amount"`
	On       string      `json:"on"`
	Document string      `json:"document,omitempty"`
	Note     string      `json:"note,omitempty"`
}

type storedOfferID struct {
	Tenant  ids.TenantID  `json:"Tenant"`
	Product ids.ProductID `json:"Product"`
}

type storedPeriod struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func storageState(s promotion.CampaignState) storedCampaignState {
	return storedCampaignState{
		ID:            s.ID,
		TenantID:      s.TenantID,
		Kind:          s.Kind,
		PlacementCode: s.PlacementCode,
		Position:      s.Position,
		Target: storedTarget{
			OfferID: storedOfferID{
				Tenant:  s.Target.OfferID.Tenant,
				Product: s.Target.OfferID.Product,
			},
			CategoryID: s.Target.CategoryID,
			SaleID:     s.Target.SaleID,
			StoreID:    s.Target.StoreID,
		},
		Period: storedPeriod{
			From: s.Period.From,
			To:   s.Period.To,
		},
		Price: s.Price,
		Marking: storedMarking{
			Advertiser: s.Marking.Advertiser,
			INN:        s.Marking.INN,
			ERID:       s.Marking.ERID,
		},
		Payment: storedPayment{
			Refund: storedRefundRecord{
				Amount:   s.Payment.Refund.Amount,
				On:       s.Payment.Refund.On,
				Document: s.Payment.Refund.Document,
				Note:     s.Payment.Refund.Note,
			},
			Status:   s.Payment.Status,
			PaidOn:   s.Payment.PaidOn,
			Amount:   s.Payment.Amount,
			Document: s.Payment.Document,
			Note:     s.Payment.Note,
		},
		Status:          s.Status,
		Version:         s.Version,
		CreatedAt:       s.CreatedAt,
		UpdatedAt:       s.UpdatedAt,
		Title:           s.Title,
		RejectionReason: s.RejectionReason,
	}
}

func domainState(s storedCampaignState) promotion.CampaignState {
	return promotion.CampaignState{
		ID:            s.ID,
		TenantID:      s.TenantID,
		Kind:          s.Kind,
		PlacementCode: s.PlacementCode,
		Position:      s.Position,
		Target: promotion.Target{
			OfferID: ids.OfferID{
				Tenant:  s.Target.OfferID.Tenant,
				Product: s.Target.OfferID.Product,
			},
			CategoryID: s.Target.CategoryID,
			SaleID:     s.Target.SaleID,
			StoreID:    s.Target.StoreID,
		},
		Period: promotion.Period{
			From: s.Period.From,
			To:   s.Period.To,
		},
		Price: s.Price,
		Marking: promotion.Marking{
			Advertiser: s.Marking.Advertiser,
			INN:        s.Marking.INN,
			ERID:       s.Marking.ERID,
		},
		Payment: promotion.Payment{
			Refund: promotion.RefundRecord{
				Amount:   s.Payment.Refund.Amount,
				On:       s.Payment.Refund.On,
				Document: s.Payment.Refund.Document,
				Note:     s.Payment.Refund.Note,
			},
			Status:   s.Payment.Status,
			PaidOn:   s.Payment.PaidOn,
			Amount:   s.Payment.Amount,
			Document: s.Payment.Document,
			Note:     s.Payment.Note,
		},
		Status:          s.Status,
		Version:         s.Version,
		CreatedAt:       s.CreatedAt,
		UpdatedAt:       s.UpdatedAt,
		Title:           s.Title,
		RejectionReason: s.RejectionReason,
	}
}
