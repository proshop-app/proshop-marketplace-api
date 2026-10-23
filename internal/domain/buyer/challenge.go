package buyer

import (
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// ChallengeStatus — состояние проверки входа.
type ChallengeStatus string

const (
	// ChallengePending — ждём звонка покупателя.
	ChallengePending ChallengeStatus = "pending"
	// ChallengeConfirmed — шлюз подтвердил звонок, сессия ещё не выдана.
	ChallengeConfirmed ChallengeStatus = "confirmed"
	// ChallengeExpired — время на звонок вышло.
	ChallengeExpired ChallengeStatus = "expired"
	// ChallengeUsed — по проверке выдана сессия; второй раз она не сработает.
	ChallengeUsed ChallengeStatus = "used"
)

// CallStatus — ответ шлюза о звонке.
type CallStatus string

const (
	// CallPending — звонка ещё не было.
	CallPending CallStatus = "pending"
	// CallConfirmed — покупатель позвонил с проверяемого номера.
	CallConfirmed CallStatus = "confirmed"
	// CallExpired — шлюз закрыл проверку по сроку.
	CallExpired CallStatus = "expired"
)

// CallInstruction — куда покупателю звонить; выдаёт шлюз.
type CallInstruction struct {
	ProviderRef     string
	CallPhone       string
	CallPhonePretty string
}

// ChallengeState — плоский снимок проверки.
type ChallengeState struct {
	ID              ids.UUID
	Phone           Phone
	ProviderRef     string
	CallPhone       string
	CallPhonePretty string
	Status          ChallengeStatus
	Checks          int
	LastCheckedAt   time.Time
	ExpiresAt       time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
	Version         int64
}

// Challenge — проверка телефона звонком. Переходы: pending → confirmed → used,
// pending → expired.
type Challenge struct {
	state  ChallengeState
	origin struct {
		status  ChallengeStatus
		version int64
	}
}

// NewChallenge — проверка, начатая у шлюза.
func NewChallenge(id ids.UUID, phone Phone, call CallInstruction, now time.Time, ttl time.Duration) *Challenge {
	return &Challenge{state: ChallengeState{
		ID:              id,
		Phone:           phone,
		ProviderRef:     call.ProviderRef,
		CallPhone:       call.CallPhone,
		CallPhonePretty: call.CallPhonePretty,
		Status:          ChallengePending,
		ExpiresAt:       now.Add(ttl),
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}}
}

// RestoreChallenge поднимает проверку из хранилища.
func RestoreChallenge(state ChallengeState) *Challenge {
	c := &Challenge{state: state}
	c.origin.status, c.origin.version = state.Status, state.Version

	return c
}

// State — снимок для хранения.
func (c *Challenge) State() ChallengeState { return c.state }

// Origin — статус и версия на момент чтения; у новой проверки версия нулевая.
func (c *Challenge) Origin() (ChallengeStatus, int64) { return c.origin.status, c.origin.version }

// MarkSaved фиксирует сохранённое состояние.
func (c *Challenge) MarkSaved() {
	c.origin.status, c.origin.version = c.state.Status, c.state.Version
}

// ID — идентификатор проверки.
func (c *Challenge) ID() ids.UUID { return c.state.ID }

// Status — состояние проверки.
func (c *Challenge) Status() ChallengeStatus { return c.state.Status }

// Phone — проверяемый телефон.
func (c *Challenge) Phone() Phone { return c.state.Phone }

// ProviderRef — идентификатор проверки у шлюза.
func (c *Challenge) ProviderRef() string { return c.state.ProviderRef }

// DueForCheck — проверку пора спросить у шлюза: она ждёт звонка, срок не
// вышел и с прошлого вопроса прошло не меньше interval.
func (c *Challenge) DueForCheck(now time.Time, interval time.Duration) bool {
	return c.state.Status == ChallengePending && now.Before(c.state.ExpiresAt) &&
		(c.state.LastCheckedAt.IsZero() || now.Sub(c.state.LastCheckedAt) >= interval)
}

// Expire закрывает ждущую проверку, если срок вышел.
func (c *Challenge) Expire(now time.Time) bool {
	if c.state.Status != ChallengePending || now.Before(c.state.ExpiresAt) {
		return false
	}

	c.state.Status = ChallengeExpired
	c.touch(now)

	return true
}

// Observe применяет ответ шлюза. Подтверждение после срока не принимается:
// покупатель позвонил слишком поздно.
func (c *Challenge) Observe(status CallStatus, now time.Time) bool {
	if c.state.Status != ChallengePending {
		return false
	}
	if c.Expire(now) {
		return true
	}

	c.state.Checks++
	c.state.LastCheckedAt = now

	switch status {
	case CallConfirmed:
		c.state.Status = ChallengeConfirmed
	case CallExpired:
		c.state.Status = ChallengeExpired
	}

	c.touch(now)

	return true
}

// Use отмечает выдачу сессии; второй раз проверка не сработает.
func (c *Challenge) Use(now time.Time) error {
	switch c.state.Status {
	case ChallengeConfirmed:
		c.state.Status = ChallengeUsed
		c.touch(now)

		return nil
	case ChallengeUsed:
		return ErrChallengeUsed
	case ChallengeExpired:
		return ErrChallengeExpired
	default:
		return ErrChallengeNotConfirmed
	}
}

func (c *Challenge) touch(now time.Time) {
	c.state.Version = c.origin.version + 1
	c.state.UpdatedAt = now
}
