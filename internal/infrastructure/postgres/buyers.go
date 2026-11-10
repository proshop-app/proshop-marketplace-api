package postgres

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	buyerByPhone           = "phone = ?"
	buyerByID              = "id = ?"
	challengeByID          = "id = ?"
	challengeByProviderRef = "provider_ref = ?"
	challengeByOrigin      = "id = ? AND status = ? AND version = ?"
)

// Buyers — покупатели в PostgreSQL.
type Buyers struct {
	db *gorm.DB
}

// Challenges — проверки входа в PostgreSQL.
type Challenges struct {
	db *gorm.DB
}

// FindByPhone — покупатель по телефону.
func (r *Buyers) FindByPhone(ctx context.Context, phone buyer.Phone) (buyer.Buyer, error) {
	return r.one(ctx, buyerByPhone, string(phone))
}

// Get — покупатель.
func (r *Buyers) Get(ctx context.Context, id ids.UUID) (buyer.Buyer, error) {
	return r.one(ctx, buyerByID, string(id))
}

// Add заводит покупателя.
func (r *Buyers) Add(ctx context.Context, b buyer.Buyer) error {
	record := buyerRecord{
		ID:        string(b.ID),
		Phone:     string(b.Phone),
		CreatedAt: b.CreatedAt,
	}

	err := r.db.WithContext(ctx).Create(&record).Error

	switch {
	case isUnique(err, "buyers_phone_key"):
		return buyer.ErrExists
	case err != nil:
		return fmt.Errorf("новый покупатель: %w", err)
	default:
		return nil
	}
}

func (r *Buyers) one(ctx context.Context, where string, arg any) (buyer.Buyer, error) {
	var record buyerRecord

	err := r.db.WithContext(ctx).Where(where, arg).Take(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return buyer.Buyer{}, buyer.ErrNotFound
	}
	if err != nil {
		return buyer.Buyer{}, fmt.Errorf("покупатель: %w", err)
	}

	return record.buyer(), nil
}

// Get — проверка.
func (r *Challenges) Get(ctx context.Context, id ids.UUID) (*buyer.Challenge, error) {
	return r.one(ctx, challengeByID, string(id))
}

// FindByProviderRef — проверка по идентификатору шлюза.
func (r *Challenges) FindByProviderRef(ctx context.Context, ref string) (*buyer.Challenge, error) {
	return r.one(ctx, challengeByProviderRef, ref)
}

// Add сохраняет новую проверку.
func (r *Challenges) Add(ctx context.Context, c *buyer.Challenge) error {
	record := newChallengeRecord(c.State())

	err := r.db.WithContext(ctx).Create(&record).Error
	if err != nil {
		return fmt.Errorf("новая проверка входа: %w", err)
	}

	c.MarkSaved()

	return nil
}

// Update сохраняет условно по исходным статусу и версии.
func (r *Challenges) Update(ctx context.Context, c *buyer.Challenge) error {
	record := newChallengeRecord(c.State())
	status, version := c.Origin()

	result := r.db.WithContext(ctx).
		Model(&challengeRecord{}).
		Where(challengeByOrigin, record.ID, string(status), version).
		Updates(map[string]any{
			"status":          record.Status,
			"checks":          record.Checks,
			"last_checked_at": record.LastCheckedAt,
			"updated_at":      record.UpdatedAt,
			"version":         record.Version,
		})
	if result.Error != nil {
		return fmt.Errorf("проверка входа %s: %w", record.ID, result.Error)
	}
	if result.RowsAffected == 0 {
		return buyer.ErrChallengeConflict
	}

	c.MarkSaved()

	return nil
}

func (r *Challenges) one(ctx context.Context, where string, arg any) (*buyer.Challenge, error) {
	var record challengeRecord

	err := r.db.WithContext(ctx).Where(where, arg).Take(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, buyer.ErrChallengeNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("проверка входа: %w", err)
	}

	return buyer.RestoreChallenge(record.state()), nil
}
