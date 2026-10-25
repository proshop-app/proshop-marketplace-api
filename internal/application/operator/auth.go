// Package operator — вход и серверная авторизация оператора платформы.
package operator

import (
	"context"
	"strings"
	"time"

	"errors"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/identity"
	"github.com/proshop-app/proshop-marketplace/internal/domain/operator"
)

// Config — отдельная учётная запись платформы, хеш вместо открытого пароля.
type Config struct {
	Principal    operator.Principal
	PasswordHash string
	SessionTTL   time.Duration
}

// Auth — сессии оператора; абсолютный срок не продлевается чтением.
type Auth struct {
	tokens    ports.IdentityTokens
	limiter   ports.RateLimiter
	passwords ports.PasswordVerifier
	ids       ports.IDs
	clock     ports.Clock
	cfg       Config
	slots     chan struct{}
}

// NewAuth собирает вход через порты, без зависимости от Redis и Argon2.
func NewAuth(
	tokens ports.IdentityTokens,
	limiter ports.RateLimiter,
	passwords ports.PasswordVerifier,
	ids ports.IDs,
	clock ports.Clock,
	cfg Config,
) *Auth {
	return &Auth{
		tokens:    tokens,
		limiter:   limiter,
		passwords: passwords,
		ids:       ids,
		clock:     clock,
		cfg:       cfg,
		slots:     make(chan struct{}, 2),
	}
}

// Login ограничивает работу Argon2 по адресу, учётной записи и всему сервису.
// Возвращённый токен предназначен только для Set-Cookie.
func (a *Auth) Login(
	ctx context.Context,
	email, password, ip, previous string,
) (operator.Principal, ports.IdentityAccess, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	for _, limit := range []struct {
		key   string
		count int
	}{
		{"global", 100},
		{"ip:" + operator.Hash(ip), 20},
		{"account:" + operator.Hash(email), 10},
	} {
		allowed, _, err := a.limiter.Allow(ctx, "operator:"+limit.key, limit.count, 10*time.Minute)
		if err != nil {
			return operator.Principal{}, ports.IdentityAccess{}, err
		}
		if !allowed {
			return operator.Principal{}, ports.IdentityAccess{}, operator.ErrLimited
		}
	}
	if a.cfg.PasswordHash == "" || len(password) == 0 || len(password) > 1024 || len(email) > 254 {
		return operator.Principal{}, ports.IdentityAccess{}, operator.ErrUnauthorized
	}
	// Неизвестный логин требует той же работы Argon2, что и неверный пароль.
	select {
	case a.slots <- struct{}{}:
	case <-ctx.Done():
		return operator.Principal{}, ports.IdentityAccess{}, ctx.Err()
	}
	valid := a.passwords.Verify(password, a.cfg.PasswordHash)
	<-a.slots
	if !valid || email != strings.ToLower(a.cfg.Principal.Email) {
		return operator.Principal{}, ports.IdentityAccess{}, operator.ErrUnauthorized
	}
	if err := operator.Require(a.cfg.Principal); err != nil {
		return operator.Principal{}, ports.IdentityAccess{}, err
	}
	if previous != "" {
		if err := a.Logout(ctx, previous); err != nil {
			return operator.Principal{}, ports.IdentityAccess{}, err
		}
	}
	access, err := a.tokens.Open(ctx, credentialSubject(a.cfg))
	if err != nil {
		return operator.Principal{}, ports.IdentityAccess{}, err
	}

	return a.cfg.Principal, access, nil
}

// Current проверяет срок, отзыв конфигурацией и актуальную роль на каждом запросе.
func (a *Auth) Current(ctx context.Context, token string) (operator.Principal, error) {
	if a == nil || a.tokens == nil || a.cfg.PasswordHash == "" || token == "" {
		return operator.Principal{}, operator.ErrUnauthorized
	}
	access, err := a.tokens.Authenticate(ctx, token)
	if err != nil {
		if errors.Is(err, identity.ErrUnauthorized) || errors.Is(err, identity.ErrAccessExpired) {
			return operator.Principal{}, operator.ErrUnauthorized
		}

		return operator.Principal{}, err
	}

	return a.principal(access)
}

// Profile разрешает silent refresh только для загрузки профиля оператора.
func (a *Auth) Profile(ctx context.Context, token, key string) (operator.Principal, ports.IdentityAccess, error) {
	if a == nil || a.tokens == nil || a.cfg.PasswordHash == "" {
		return operator.Principal{}, ports.IdentityAccess{}, operator.ErrUnauthorized
	}

	access, err := a.tokens.Bootstrap(ctx, token, key)
	if err != nil {
		return operator.Principal{}, ports.IdentityAccess{}, err
	}

	principal, err := a.principal(access)
	if err != nil && access.Key != "" {
		_ = a.tokens.Logout(ctx, access.Key)
	}

	return principal, access, err
}

// Refresh выдаёт новый access, проверяя живые полномочия конфигурации.
func (a *Auth) Refresh(ctx context.Context, key string) (operator.Principal, ports.IdentityAccess, error) {
	if a == nil || a.tokens == nil || a.cfg.PasswordHash == "" {
		return operator.Principal{}, ports.IdentityAccess{}, operator.ErrUnauthorized
	}

	access, err := a.tokens.Refresh(ctx, key)
	if err != nil {
		return operator.Principal{}, ports.IdentityAccess{}, err
	}

	principal, err := a.principal(access)
	if err != nil && access.Key != "" {
		_ = a.tokens.Logout(ctx, access.Key)
	}

	return principal, access, err
}

func (a *Auth) principal(access ports.IdentityAccess) (operator.Principal, error) {
	if access.LocalID != credentialSubject(a.cfg) || a.cfg.PasswordHash == "" {
		return operator.Principal{}, operator.ErrUnauthorized
	}
	if err := operator.Require(a.cfg.Principal); err != nil {
		return operator.Principal{}, err
	}

	return a.cfg.Principal, nil
}

// Logout отзывает только refresh; JWT проверяется stateless до exp.
func (a *Auth) Logout(ctx context.Context, key string) error {
	if a == nil || key == "" {
		return nil
	}

	return a.tokens.Logout(ctx, key)
}

// TTL — абсолютное окно refresh оператора.
func (a *Auth) TTL() time.Duration { return a.cfg.SessionTTL }
