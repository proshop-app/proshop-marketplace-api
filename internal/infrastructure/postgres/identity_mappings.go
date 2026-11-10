package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

const (
	bindBuyerIdentity = `
        INSERT INTO identity_bindings (issuer, client_id, subject, local_id)
        VALUES ($1, $2, $3, $4)
        ON CONFLICT (issuer, client_id, subject) DO NOTHING`
	findBuyerIdentity = `
        SELECT local_id FROM identity_bindings
        WHERE issuer = $1 AND client_id = $2 AND subject = $3`
)

// ErrIdentityStorage скрывает текст SQL и параметры.
var ErrIdentityStorage = errs.New(errs.Unavailable, "identity.mapping_unavailable", "данные входа недоступны")

// IdentityMappings — scope задан конфигурацией, а не содержимым JWT.
type IdentityMappings struct {
	source *connectionSource
	issuer string
	client string
}

// NewIdentityMappings отделяет mapping по issuer и backend-клиенту.
func NewIdentityMappings(pool *pgxpool.Pool, issuer, client string) *IdentityMappings {
	return &IdentityMappings{
		source: newConnectionSource(pool),
		issuer: issuer,
		client: client,
	}
}

// Bind никогда не перепривязывает уже известный hub subject.
func (s *IdentityMappings) Bind(ctx context.Context, subject string, id string) error {
	err := s.source.withConnection(ctx, func(db *gorm.DB) error {
		if _, err := execSQL(ctx, db, bindBuyerIdentity, s.issuer, s.client, subject, id); err != nil {
			return ErrIdentityStorage
		}

		found, err := s.find(ctx, db, subject)
		if err != nil {
			return err
		}
		if found != id {
			return buyer.ErrSessionNotFound
		}

		return nil
	})
	if err != nil && !errors.Is(err, buyer.ErrSessionNotFound) {
		return ErrIdentityStorage
	}

	return err
}

// Find не ищет пользователя по телефону или почте.
func (s *IdentityMappings) Find(ctx context.Context, subject string) (string, error) {
	var id string

	err := s.source.withConnection(ctx, func(db *gorm.DB) error {
		var err error
		id, err = s.find(ctx, db, subject)
		return err
	})
	if err != nil && !errors.Is(err, buyer.ErrSessionNotFound) {
		return "", ErrIdentityStorage
	}

	return id, err
}

func (s *IdentityMappings) find(ctx context.Context, db *gorm.DB, subject string) (string, error) {
	var id string
	err := rawSQL(ctx, db, findBuyerIdentity, s.issuer, s.client, subject).Row().Scan(&id)
	if notFound(err) {
		return "", buyer.ErrSessionNotFound
	}
	if err != nil {
		return "", ErrIdentityStorage
	}

	return id, nil
}
