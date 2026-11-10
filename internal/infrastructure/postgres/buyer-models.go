package postgres

import (
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	buyersTable     = "buyers"
	challengesTable = "phone_challenges"
)

type buyerRecord struct {
	ID        string    `gorm:"type:uuid;primaryKey"`
	Phone     string    `gorm:"column:phone"`
	CreatedAt time.Time `gorm:"autoCreateTime:false"`
}

type challengeRecord struct {
	ID              string     `gorm:"type:uuid;primaryKey"`
	Phone           string     `gorm:"column:phone"`
	ProviderRef     string     `gorm:"column:provider_ref"`
	CallPhone       string     `gorm:"column:call_phone"`
	CallPhonePretty string     `gorm:"column:call_phone_pretty"`
	Status          string     `gorm:"column:status"`
	Checks          int        `gorm:"column:checks"`
	LastCheckedAt   *time.Time `gorm:"column:last_checked_at"`
	ExpiresAt       time.Time  `gorm:"column:expires_at"`
	CreatedAt       time.Time  `gorm:"autoCreateTime:false"`
	UpdatedAt       time.Time  `gorm:"autoUpdateTime:false"`
	Version         int64      `gorm:"column:version"`
}

func (buyerRecord) TableName() string {
	return buyersTable
}

func (r buyerRecord) buyer() buyer.Buyer {
	return buyer.Buyer{
		ID:        ids.UUID(r.ID),
		Phone:     buyer.Phone(r.Phone),
		CreatedAt: r.CreatedAt,
	}
}

func (challengeRecord) TableName() string {
	return challengesTable
}

func newChallengeRecord(state buyer.ChallengeState) challengeRecord {
	record := challengeRecord{
		ID:              string(state.ID),
		Phone:           string(state.Phone),
		ProviderRef:     state.ProviderRef,
		CallPhone:       state.CallPhone,
		CallPhonePretty: state.CallPhonePretty,
		Status:          string(state.Status),
		Checks:          state.Checks,
		ExpiresAt:       state.ExpiresAt,
		CreatedAt:       state.CreatedAt,
		UpdatedAt:       state.UpdatedAt,
		Version:         state.Version,
	}
	if !state.LastCheckedAt.IsZero() {
		record.LastCheckedAt = &state.LastCheckedAt
	}

	return record
}

func (r challengeRecord) state() buyer.ChallengeState {
	state := buyer.ChallengeState{
		ID:              ids.UUID(r.ID),
		Phone:           buyer.Phone(r.Phone),
		ProviderRef:     r.ProviderRef,
		CallPhone:       r.CallPhone,
		CallPhonePretty: r.CallPhonePretty,
		Status:          buyer.ChallengeStatus(r.Status),
		Checks:          r.Checks,
		ExpiresAt:       r.ExpiresAt,
		CreatedAt:       r.CreatedAt,
		UpdatedAt:       r.UpdatedAt,
		Version:         r.Version,
	}
	if r.LastCheckedAt != nil {
		state.LastCheckedAt = *r.LastCheckedAt
	}

	return state
}
