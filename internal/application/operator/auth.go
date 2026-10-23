// Package operator — вход и серверная авторизация оператора платформы.
package operator

import (
	"context"
	"strings"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
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
	sessions  ports.OperatorSessions
	limiter   ports.RateLimiter
	passwords ports.PasswordVerifier
	ids       ports.IDs
	clock     ports.Clock
	cfg       Config
	slots     chan struct{}
}

// NewAuth собирает вход через порты, без зависимости от Redis и Argon2.
func NewAuth(
	sessions ports.OperatorSessions,
	limiter ports.RateLimiter,
	passwords ports.PasswordVerifier,
	ids ports.IDs,
	clock ports.Clock,
	cfg Config,
) *Auth {
	return &Auth{
		sessions:  sessions,
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
func (a *Auth) Login(ctx context.Context, email, password, ip, previous string) (operator.Principal, string, error) {
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
			return operator.Principal{}, "", err
		}
		if !allowed {
			return operator.Principal{}, "", operator.ErrLimited
		}
	}
	if a.cfg.PasswordHash == "" || len(password) == 0 || len(password) > 1024 || len(email) > 254 {
		return operator.Principal{}, "", operator.ErrUnauthorized
	}
	// Неизвестный логин требует той же работы Argon2, что и неверный пароль.
	select {
	case a.slots <- struct{}{}:
	case <-ctx.Done():
		return operator.Principal{}, "", ctx.Err()
	}
	valid := a.passwords.Verify(password, a.cfg.PasswordHash)
	<-a.slots
	if !valid || email != strings.ToLower(a.cfg.Principal.Email) {
		return operator.Principal{}, "", operator.ErrUnauthorized
	}
	if err := operator.Require(a.cfg.Principal); err != nil {
		return operator.Principal{}, "", err
	}
	if previous != "" {
		if err := a.Logout(ctx, previous); err != nil {
			return operator.Principal{}, "", err
		}
	}
	token := a.ids.NewToken()
	session := operator.Session{
		TokenHash:      operator.Hash(token),
		Principal:      a.cfg.Principal,
		CredentialHash: operator.Hash(a.cfg.PasswordHash),
		ExpiresAt:      a.clock.Now().Add(a.cfg.SessionTTL),
	}
	if err := a.sessions.Save(ctx, session); err != nil {
		return operator.Principal{}, "", err
	}
	return session.Principal, token, nil
}

// Current проверяет срок, отзыв конфигурацией и актуальную роль на каждом запросе.
func (a *Auth) Current(ctx context.Context, token string) (operator.Principal, error) {
	if a == nil || token == "" {
		return operator.Principal{}, operator.ErrUnauthorized
	}
	session, err := a.sessions.Get(ctx, operator.Hash(token))
	if err != nil {
		return operator.Principal{}, err
	}
	if !a.clock.Now().Before(session.ExpiresAt) ||
		session.CredentialHash != operator.Hash(a.cfg.PasswordHash) || session.Principal != a.cfg.Principal {
		return operator.Principal{}, operator.ErrUnauthorized
	}
	if err := operator.Require(session.Principal); err != nil {
		return operator.Principal{}, err
	}
	return session.Principal, nil
}

// Logout отзывает сессию; повторный выход также успешен.
func (a *Auth) Logout(ctx context.Context, token string) error {
	if a == nil || token == "" {
		return nil
	}
	return a.sessions.Delete(ctx, operator.Hash(token))
}

// TTL — срок cookie совпадает со сроком серверной сессии.
func (a *Auth) TTL() time.Duration { return a.cfg.SessionTTL }
