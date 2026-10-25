// Package identity — JWT-доступ приложений и защищённая ротация refresh хаба.
package identity

import (
	"context"
	"errors"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/identity"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

// ErrRefreshBusy — другой экземпляр уже меняет refresh; прежний секрет не повторяется.
var ErrRefreshBusy = errs.New(errs.Unavailable, "buyer.refresh_busy", "обновление доступа уже выполняется")

const refreshCleanupTimeout = 2 * time.Second

// Tokens — сценарии JWT-доступа и серверного хранения refresh.
type Tokens struct {
	hub        ports.IdentityHub
	identities ports.IdentityMappings
	refreshes  ports.IdentityRefreshStore
	cipher     ports.RefreshCipher
	ids        ports.IDs
	clock      ports.Clock
	ttl        time.Duration
	flights    singleflight.Group
}

// NewTokens собирает сценарии без зависимости приложения от инфраструктуры.
func NewTokens(
	hub ports.IdentityHub,
	identities ports.IdentityMappings,
	refreshes ports.IdentityRefreshStore,
	cipher ports.RefreshCipher,
	generator ports.IDs,
	clock ports.Clock,
	ttl time.Duration,
) *Tokens {
	return &Tokens{
		hub:        hub,
		identities: identities,
		refreshes:  refreshes,
		cipher:     cipher,
		ids:        generator,
		clock:      clock,
		ttl:        ttl,
	}
}

// Open вызывается только после подтверждения локального способа входа.
func (s *Tokens) Open(ctx context.Context, id string) (ports.IdentityAccess, error) {
	bundle, err := s.hub.Open(ctx, id)
	if err != nil {
		return ports.IdentityAccess{}, err
	}

	if err := s.identities.Bind(ctx, bundle.Claims.Subject, id); err != nil {
		_ = s.hub.Revoke(ctx, bundle.Refresh)

		return ports.IdentityAccess{}, err
	}

	key := s.ids.NewToken()
	hash := identity.HashToken(key)
	encrypted, err := s.cipher.Seal(bundle.Refresh, hash)
	if err != nil {
		_ = s.hub.Revoke(ctx, bundle.Refresh)

		return ports.IdentityAccess{}, err
	}

	end := s.clock.Now().Add(s.ttl)
	record := ports.IdentityRefresh{
		LocalID:   id,
		Subject:   bundle.Claims.Subject,
		Encrypted: encrypted,
		ExpiresAt: end,
	}
	if err := s.refreshes.Save(ctx, hash, record); err != nil {
		_ = s.hub.Revoke(ctx, bundle.Refresh)

		return ports.IdentityAccess{}, err
	}

	return accessOf(bundle, key, id, end), nil
}

// Authenticate не читает Redis и не обращается за userinfo.
func (s *Tokens) Authenticate(ctx context.Context, token string) (ports.IdentityAccess, error) {
	claims, err := s.hub.Claims(ctx, token, false)
	if err != nil {
		return ports.IdentityAccess{}, err
	}

	id, err := s.identities.Find(ctx, claims.Subject)
	if err != nil {
		return ports.IdentityAccess{}, err
	}

	return ports.IdentityAccess{
		LocalID: id,

		AccessExpiresAt: claims.ExpiresAt,
	}, nil
}

// Bootstrap обновляет только отсутствующий либо проверенный истёкший JWT.
func (s *Tokens) Bootstrap(ctx context.Context, token, key string) (ports.IdentityAccess, error) {
	if token != "" {
		session, err := s.Authenticate(ctx, token)
		if err == nil {
			return ports.IdentityAccess{
				Token:           token,
				LocalID:         session.LocalID,
				AccessExpiresAt: session.AccessExpiresAt,
			}, nil
		}

		if !errors.Is(err, identity.ErrAccessExpired) {
			return ports.IdentityAccess{}, err
		}
	}

	// Проверенный истёкший access не должен обновиться под чужим аккаунтом.
	if token != "" {
		claims, err := s.hub.Claims(ctx, token, true)
		if err != nil {
			return ports.IdentityAccess{}, err
		}

		id, err := s.identities.Find(ctx, claims.Subject)
		if err != nil {
			return ports.IdentityAccess{}, err
		}

		return s.refresh(ctx, key, id)
	}

	return s.Refresh(ctx, key)
}

// Refresh объединяет обмены внутри процесса; между экземплярами — атомарный Claim.
func (s *Tokens) Refresh(ctx context.Context, key string) (ports.IdentityAccess, error) {
	return s.refresh(ctx, key, "")
}

func (s *Tokens) refresh(ctx context.Context, key string, expected string) (ports.IdentityAccess, error) {
	if key == "" {
		return ports.IdentityAccess{}, identity.ErrUnauthorized
	}

	// Ожидаемый аккаунт входит в ключ: чужой bootstrap не присоединяется к обмену.
	result := s.flights.DoChan(identity.HashToken(key)+":"+expected, func() (any, error) {
		return s.exchange(ctx, key, expected)
	})

	select {
	case <-ctx.Done():
		return ports.IdentityAccess{}, ctx.Err()
	case value := <-result:
		if value.Err != nil {
			return ports.IdentityAccess{}, value.Err
		}

		return value.Val.(ports.IdentityAccess), nil
	}
}

func (s *Tokens) exchange(ctx context.Context, key string, expected string) (ports.IdentityAccess, error) {
	hash := identity.HashToken(key)
	owner := s.ids.NewToken()
	record, err := s.refreshes.Claim(ctx, hash, owner, expected)
	if err != nil {
		return ports.IdentityAccess{}, err
	}

	// После отправки обмена запись можно только завершить либо закрыть:
	// неопределённый исход никогда не разрешает повтор прежнего refresh.
	if !s.clock.Now().Before(record.ExpiresAt) || (expected != "" && record.LocalID != expected) {
		_, _ = s.refreshes.Delete(ctx, hash)

		return ports.IdentityAccess{}, identity.ErrUnauthorized
	}

	id, err := s.identities.Find(ctx, record.Subject)
	if err != nil || id != record.LocalID {
		s.releaseBeforeExchange(ctx, hash, owner, record)
		if err != nil {
			return ports.IdentityAccess{}, err
		}

		return ports.IdentityAccess{}, identity.ErrUnauthorized
	}

	plain, err := s.cipher.Open(record.Encrypted, hash)
	if err != nil {
		s.releaseBeforeExchange(ctx, hash, owner, record)
		return ports.IdentityAccess{}, err
	}

	bundle, err := s.hub.Refresh(ctx, plain, record.Subject)
	if err != nil {
		s.invalidateAfterExchange(ctx, hash)

		return ports.IdentityAccess{}, identity.ErrUnauthorized
	}

	record.Encrypted, err = s.cipher.Seal(bundle.Refresh, hash)
	if err != nil {
		_ = s.hub.Revoke(ctx, bundle.Refresh)
		s.invalidateAfterExchange(ctx, hash)

		return ports.IdentityAccess{}, identity.ErrUnauthorized
	}

	if err := s.refreshes.Finish(ctx, hash, owner, record); err != nil {
		_ = s.hub.Revoke(ctx, bundle.Refresh)
		s.invalidateAfterExchange(ctx, hash)

		return ports.IdentityAccess{}, identity.ErrUnauthorized
	}

	return accessOf(bundle, key, record.LocalID, record.ExpiresAt), nil
}

// Logout удаляет локальный refresh до сетевого отзыва; Finish не воскресит запись.
func (s *Tokens) Logout(ctx context.Context, key string) error {
	if key == "" {
		return nil
	}

	hash := identity.HashToken(key)
	record, err := s.refreshes.Delete(ctx, hash)
	if errors.Is(err, identity.ErrUnauthorized) {
		return nil
	}
	if err != nil {
		return err
	}

	plain, err := s.cipher.Open(record.Encrypted, hash)
	if err != nil {
		return err
	}

	return s.hub.Revoke(ctx, plain)
}

func accessOf(bundle ports.HubTokens, key string, id string, end time.Time) ports.IdentityAccess {
	return ports.IdentityAccess{
		Token:           bundle.Access,
		Key:             key,
		LocalID:         id,
		AccessExpiresAt: bundle.Claims.ExpiresAt,
		ExpiresAt:       end,
	}
}

// releaseBeforeExchange безопасен только пока запрос ротации не отправлялся.
func (s *Tokens) releaseBeforeExchange(ctx context.Context, hash, owner string, record ports.IdentityRefresh) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshCleanupTimeout)
	defer cancel()

	_ = s.refreshes.Finish(cleanup, hash, owner, record)
}

// Неопределённый обмен требует нового входа, даже если HTTP-запрос уже отменён.
func (s *Tokens) invalidateAfterExchange(ctx context.Context, hash string) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshCleanupTimeout)
	defer cancel()

	_, _ = s.refreshes.Delete(cleanup, hash)
}
