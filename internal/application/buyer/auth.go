// Package buyer — вход покупателя по звонку и сессии.
package buyer

import (
	"context"
	"errors"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// ErrCallCheckFailed — шлюз отказал в проверке.
var ErrCallCheckFailed = errs.New(
	errs.Unavailable,
	"buyer.call_check_failed",
	"проверка звонком сейчас недоступна, попробуйте позже",
)

// Limit — не больше Count попыток за Window.
type Limit struct {
	Count  int
	Window time.Duration
}

// AuthConfig — сроки и лимиты входа.
type AuthConfig struct {
	ChallengeTTL time.Duration
	// CheckInterval — не чаще стольких опросов шлюза по одной проверке.
	CheckInterval time.Duration
	SessionTTL    time.Duration
	PhoneLimit    Limit
	IPLimit       Limit
}

// ChallengeView — проверка для покупателя.
type ChallengeView struct {
	ID              ids.UUID
	Status          buyer.ChallengeStatus
	CallPhone       string
	CallPhonePretty string
	ExpiresAt       time.Time
}

// IssuedSession — выданная сессия; токен виден только здесь.
type IssuedSession struct {
	Token           string
	Key             string
	AccessExpiresAt time.Time
	ExpiresAt       time.Time
	Buyer           buyer.Buyer
}

// CheckResult — состояние проверки и сессия, если вход состоялся.
type CheckResult struct {
	Challenge ChallengeView
	Session   *IssuedSession
}

// Auth — вход покупателя по звонку.
type Auth struct {
	uow     ports.UnitOfWork
	calls   ports.CallCheck
	tokens  ports.IdentityTokens
	limiter ports.RateLimiter
	ids     ports.IDs
	clock   ports.Clock
	config  AuthConfig
}

// NewAuth — сценарии входа.
func NewAuth(uow ports.UnitOfWork, calls ports.CallCheck, tokens ports.IdentityTokens, limiter ports.RateLimiter,
	identities ports.IDs, clock ports.Clock, config AuthConfig) *Auth {
	return &Auth{
		uow:     uow,
		calls:   calls,
		tokens:  tokens,
		limiter: limiter,
		ids:     identities,
		clock:   clock,
		config:  config,
	}
}

// StartLogin начинает вход: лимиты по телефону и адресу, проверка у шлюза.
func (a *Auth) StartLogin(ctx context.Context, rawPhone, ip string) (ChallengeView, error) {
	phone, err := buyer.ParsePhone(rawPhone)
	if err != nil {
		return ChallengeView{}, err
	}

	if err := a.limit(ctx, "login:phone:"+string(phone), a.config.PhoneLimit); err != nil {
		return ChallengeView{}, err
	}
	if ip != "" {
		if err := a.limit(ctx, "login:ip:"+ip, a.config.IPLimit); err != nil {
			return ChallengeView{}, err
		}
	}

	instruction, err := a.calls.Start(ctx, phone, ip)
	if err != nil {
		var typed *errs.Error
		if errors.As(err, &typed) {
			return ChallengeView{}, err
		}

		return ChallengeView{}, ErrCallCheckFailed.Wrap(err)
	}

	challenge := buyer.NewChallenge(a.ids.NewUUID(), phone, instruction, a.clock.Now(), a.config.ChallengeTTL)

	err = a.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.Challenges().Add(ctx, challenge)
	})
	if err != nil {
		return ChallengeView{}, err
	}

	return viewOf(challenge), nil
}

// CheckLogin — состояние проверки. Пора — спрашивает шлюз; звонок подтверждён
// — выдаёт сессию, один раз на проверку.
func (a *Auth) CheckLogin(ctx context.Context, challengeID ids.UUID) (CheckResult, error) {
	challenge, err := a.challenge(ctx, challengeID)
	if err != nil {
		return CheckResult{}, err
	}

	now := a.clock.Now()

	switch {
	case challenge.DueForCheck(now, a.config.CheckInterval):
		status, answered := a.ask(ctx, challenge.ProviderRef())
		if !answered {
			return CheckResult{Challenge: viewOf(challenge)}, nil
		}

		if challenge, err = a.observe(ctx, challengeID, status); err != nil {
			return CheckResult{}, err
		}
	case challenge.Status() == buyer.ChallengePending && challenge.Expire(now):
		if err := a.save(ctx, challenge); err != nil {
			return CheckResult{}, err
		}
	}

	switch challenge.Status() {
	case buyer.ChallengeConfirmed:
		return a.issue(ctx, challengeID)
	case buyer.ChallengeUsed:
		return CheckResult{}, buyer.ErrChallengeUsed
	default:
		return CheckResult{Challenge: viewOf(challenge)}, nil
	}
}

// RefreshByProvider — оповещение шлюза о проверке. Телу оповещения не
// верим: состояние перечитывается у шлюза.
func (a *Auth) RefreshByProvider(ctx context.Context, providerRef string) error {
	var challenge *buyer.Challenge

	err := a.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		challenge, err = tx.Challenges().FindByProviderRef(ctx, providerRef)

		return err
	})
	if errors.Is(err, buyer.ErrChallengeNotFound) {
		return nil
	}
	if err != nil || challenge.Status() != buyer.ChallengePending {
		return err
	}

	status, err := a.calls.Status(ctx, providerRef)
	if err != nil {
		return err
	}

	_, err = a.observe(ctx, challenge.ID(), status)

	return err
}

// Authenticate — локальная проверка JWT и существования покупателя.
func (a *Auth) Authenticate(ctx context.Context, token string) (buyer.Session, error) {
	session, err := a.tokens.Authenticate(ctx, token)
	if err != nil {
		return buyer.Session{}, err
	}

	if _, err := a.Me(ctx, ids.UUID(session.LocalID)); err != nil {
		if errors.Is(err, buyer.ErrNotFound) {
			return buyer.Session{}, buyer.ErrSessionNotFound
		}

		return buyer.Session{}, err
	}

	return buyer.Session{
		BuyerID:   ids.UUID(session.LocalID),
		ExpiresAt: session.AccessExpiresAt,
	}, nil
}

// Logout отзывает refresh, не пытаясь превратить JWT в серверную сессию.
func (a *Auth) Logout(ctx context.Context, key string) error {
	return a.tokens.Logout(ctx, key)
}

// Refresh обновляет access отдельным браузерным ключом.
func (a *Auth) Refresh(ctx context.Context, key string) (IssuedSession, error) {
	access, err := a.tokens.Refresh(ctx, key)
	if err != nil {
		return IssuedSession{}, err
	}

	return a.issued(ctx, access)
}

// Bootstrap разрешён только ручке me; бизнес-запросы refresh не запускают.
func (a *Auth) Bootstrap(ctx context.Context, token, key string) (IssuedSession, error) {
	access, err := a.tokens.Bootstrap(ctx, token, key)
	if err != nil {
		return IssuedSession{}, err
	}

	return a.issued(ctx, access)
}

func (a *Auth) issued(ctx context.Context, access ports.IdentityAccess) (IssuedSession, error) {
	found, err := a.Me(ctx, ids.UUID(access.LocalID))
	if err != nil {
		if errors.Is(err, buyer.ErrNotFound) {
			return IssuedSession{}, buyer.ErrSessionNotFound
		}

		return IssuedSession{}, err
	}

	return IssuedSession{
		Token:           access.Token,
		Key:             access.Key,
		AccessExpiresAt: access.AccessExpiresAt,
		ExpiresAt:       access.ExpiresAt,
		Buyer:           found,
	}, nil
}

// Me — покупатель сессии.
func (a *Auth) Me(ctx context.Context, buyerID ids.UUID) (buyer.Buyer, error) {
	var found buyer.Buyer

	err := a.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		found, err = tx.Buyers().Get(ctx, buyerID)

		return err
	})

	return found, err
}

// ask спрашивает шлюз. Сбой шлюза — «ещё ждём»: покупатель, который уже
// звонит, не теряет вход из-за одного неудачного запроса.
func (a *Auth) ask(ctx context.Context, providerRef string) (buyer.CallStatus, bool) {
	status, err := a.calls.Status(ctx, providerRef)

	return status, err == nil
}

func (a *Auth) observe(ctx context.Context, challengeID ids.UUID, status buyer.CallStatus) (*buyer.Challenge, error) {
	var challenge *buyer.Challenge

	err := a.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		if challenge, err = tx.Challenges().Get(ctx, challengeID); err != nil {
			return err
		}

		if !challenge.Observe(status, a.clock.Now()) {
			return nil
		}

		return tx.Challenges().Update(ctx, challenge)
	})
	if errors.Is(err, buyer.ErrChallengeConflict) {
		// параллельный опрос уже записал ответ шлюза — берём его
		return a.challenge(ctx, challengeID)
	}

	return challenge, err
}

// issue отмечает проверку использованной, находит или заводит покупателя и
// выдаёт сессию. Параллельный опрос той же проверки проиграет условное
// обновление и сессии не получит.
func (a *Auth) issue(ctx context.Context, challengeID ids.UUID) (CheckResult, error) {
	now := a.clock.Now()

	var (
		challenge *buyer.Challenge
		found     buyer.Buyer
	)

	err := a.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		if challenge, err = tx.Challenges().Get(ctx, challengeID); err != nil {
			return err
		}
		if err := challenge.Use(now); err != nil {
			return err
		}
		if err := tx.Challenges().Update(ctx, challenge); err != nil {
			return err
		}

		found, err = tx.Buyers().FindByPhone(ctx, challenge.Phone())
		if errors.Is(err, buyer.ErrNotFound) {
			found = buyer.New(a.ids.NewUUID(), challenge.Phone(), now)
			err = tx.Buyers().Add(ctx, found)
		}

		return err
	})
	if errors.Is(err, buyer.ErrChallengeConflict) {
		return CheckResult{}, buyer.ErrChallengeUsed
	}
	if errors.Is(err, buyer.ErrExists) {
		// покупателя с этим телефоном завели параллельно: вход повторяется уже с ним
		return CheckResult{}, buyer.ErrChallengeConflict
	}
	if err != nil {
		return CheckResult{}, err
	}

	access, err := a.tokens.Open(ctx, string(found.ID))
	if err != nil {
		return CheckResult{}, err
	}

	return CheckResult{
		Challenge: viewOf(challenge),
		Session: &IssuedSession{
			Token:           access.Token,
			Key:             access.Key,
			AccessExpiresAt: access.AccessExpiresAt,
			ExpiresAt:       access.ExpiresAt,
			Buyer:           found,
		},
	}, nil
}

func (a *Auth) challenge(ctx context.Context, challengeID ids.UUID) (*buyer.Challenge, error) {
	var challenge *buyer.Challenge

	err := a.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		challenge, err = tx.Challenges().Get(ctx, challengeID)

		return err
	})

	return challenge, err
}

func (a *Auth) save(ctx context.Context, challenge *buyer.Challenge) error {
	err := a.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.Challenges().Update(ctx, challenge)
	})
	if errors.Is(err, buyer.ErrChallengeConflict) {
		return nil
	}

	return err
}

func (a *Auth) limit(ctx context.Context, key string, limit Limit) error {
	allowed, retryAfter, err := a.limiter.Allow(ctx, key, limit.Count, limit.Window)
	if err != nil {
		return err
	}
	if !allowed {
		message := "слишком много попыток входа, повторите через " + retryAfter.Round(time.Second).String()

		return buyer.ErrTooManyAttempts.WithMessage(message)
	}

	return nil
}

func viewOf(c *buyer.Challenge) ChallengeView {
	state := c.State()

	return ChallengeView{
		ID:              state.ID,
		Status:          state.Status,
		CallPhone:       state.CallPhone,
		CallPhonePretty: state.CallPhonePretty,
		ExpiresAt:       state.ExpiresAt,
	}
}
