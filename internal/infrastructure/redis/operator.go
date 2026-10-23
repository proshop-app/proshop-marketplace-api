package redis

import (
	"context"
	"encoding/json"
	"errors"

	goredis "github.com/redis/go-redis/v9"

	"github.com/proshop-app/proshop-marketplace/internal/domain/operator"
)

// OperatorSessions — отдельное пространство ключей с абсолютным TTL.
type OperatorSessions struct {
	client *goredis.Client
	prefix string
}

// NewOperatorSessions не пересекается с покупательскими сессиями.
func NewOperatorSessions(client *goredis.Client, prefix string) *OperatorSessions {
	return &OperatorSessions{client: client, prefix: prefix + "operator:session:"}
}

// Save сохраняет только отпечаток непрозрачного токена.
func (s *OperatorSessions) Save(ctx context.Context, session operator.Session) error {
	raw, err := json.Marshal(session)
	if err != nil {
		return err
	}
	err = s.client.SetArgs(ctx, s.prefix+session.TokenHash, raw, goredis.SetArgs{ExpireAt: session.ExpiresAt}).Err()
	if err != nil {
		return ErrUnavailable.Wrap(err)
	}
	return nil
}

// Get возвращает сессию по отпечатку.
func (s *OperatorSessions) Get(ctx context.Context, hash string) (operator.Session, error) {
	var session operator.Session
	raw, err := s.client.Get(ctx, s.prefix+hash).Bytes()
	if errors.Is(err, goredis.Nil) {
		return session, operator.ErrUnauthorized
	}
	if err != nil {
		return session, ErrUnavailable.Wrap(err)
	}
	if err := json.Unmarshal(raw, &session); err != nil {
		return session, ErrUnavailable.Wrap(err)
	}
	return session, nil
}

// Delete отзывает сессию.
func (s *OperatorSessions) Delete(ctx context.Context, hash string) error {
	if err := s.client.Del(ctx, s.prefix+hash).Err(); err != nil {
		return ErrUnavailable.Wrap(err)
	}
	return nil
}
