package fake

import (
	"context"
	"sync"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
)

// IdentityTokens — контракт выдачи для тестов сценариев, без настоящей криптографии.
type IdentityTokens struct {
	mu        sync.Mutex
	clock     ports.Clock
	ttl       time.Duration
	generator IDs
	access    map[string]ports.IdentityAccess
	keys      map[string]ports.IdentityAccess
	SaveErr   error
}

// NewIdentityTokens создаёт независимый от Redis и hub двойник порта.
func NewIdentityTokens(clock ports.Clock, window ...time.Duration) *IdentityTokens {
	ttl := 30 * 24 * time.Hour
	if len(window) > 0 {
		ttl = window[0]
	}
	return &IdentityTokens{
		clock:  clock,
		ttl:    ttl,
		access: make(map[string]ports.IdentityAccess),
		keys:   make(map[string]ports.IdentityAccess),
	}
}

// Open выдаёт отдельные access и ключ продления.
func (s *IdentityTokens) Open(_ context.Context, id string) (ports.IdentityAccess, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.SaveErr != nil {
		return ports.IdentityAccess{}, s.SaveErr
	}

	access := ports.IdentityAccess{
		Token:           s.generator.NewToken(),
		Key:             s.generator.NewToken(),
		LocalID:         id,
		AccessExpiresAt: s.clock.Now().Add(5 * time.Minute),
		ExpiresAt:       s.clock.Now().Add(s.ttl),
	}
	s.access[access.Token] = access
	s.keys[access.Key] = access

	return access, nil
}

// Authenticate не зависит от наличия ключа продления.
func (s *IdentityTokens) Authenticate(_ context.Context, token string) (ports.IdentityAccess, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	access, ok := s.access[token]
	if !ok {
		return ports.IdentityAccess{}, buyer.ErrSessionNotFound
	}
	if !s.clock.Now().Before(access.AccessExpiresAt) {
		return ports.IdentityAccess{}, buyer.ErrAccessExpired
	}

	return ports.IdentityAccess{
		LocalID:         access.LocalID,
		AccessExpiresAt: access.AccessExpiresAt,
	}, nil
}

// Refresh сохраняет абсолютный срок.
func (s *IdentityTokens) Refresh(_ context.Context, key string) (ports.IdentityAccess, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	access, ok := s.keys[key]
	if !ok || !s.clock.Now().Before(access.ExpiresAt) {
		return ports.IdentityAccess{}, buyer.ErrSessionNotFound
	}

	access.Token = s.generator.NewToken()
	access.AccessExpiresAt = s.clock.Now().Add(5 * time.Minute)
	s.keys[key] = access
	s.access[access.Token] = access

	return access, nil
}

// Bootstrap имитирует только допустимые ветки контракта.
func (s *IdentityTokens) Bootstrap(ctx context.Context, token, key string) (ports.IdentityAccess, error) {
	if token != "" {
		session, err := s.Authenticate(ctx, token)
		if err == nil {
			return ports.IdentityAccess{
				Token:           token,
				LocalID:         session.LocalID,
				AccessExpiresAt: session.AccessExpiresAt,
			}, nil
		}

		s.mu.Lock()
		prior, known := s.access[token]
		refresh, exists := s.keys[key]
		s.mu.Unlock()
		if !known || !exists || prior.LocalID != refresh.LocalID {
			return ports.IdentityAccess{}, buyer.ErrSessionNotFound
		}
	}

	return s.Refresh(ctx, key)
}

// Logout не отзывает уже выданные access JWT.
func (s *IdentityTokens) Logout(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.keys, key)

	return nil
}

// Count — количество refresh-записей.
func (s *IdentityTokens) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.keys)
}
