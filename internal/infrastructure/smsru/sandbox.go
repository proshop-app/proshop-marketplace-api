package smsru

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
)

// Sandbox — шлюз для разработки без звонков: любая проверка подтверждается
// сама через confirmAfter. Только для стендов разработки: на бою вход по
// любому номеру — захват чужого аккаунта.
type Sandbox struct {
	mu           sync.Mutex
	started      map[string]time.Time
	confirmAfter time.Duration
	now          func() time.Time
	counter      int
}

// NewSandbox — песочница с задержкой подтверждения.
func NewSandbox(confirmAfter time.Duration) *Sandbox {
	return &Sandbox{started: map[string]time.Time{}, confirmAfter: confirmAfter, now: time.Now}
}

// Start заводит проверку sandbox-N.
func (s *Sandbox) Start(_ context.Context, _ buyer.Phone, _ string) (buyer.CallInstruction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.counter++
	ref := fmt.Sprintf("sandbox-%d-%d", s.now().UnixNano(), s.counter)
	s.started[ref] = s.now()

	return buyer.CallInstruction{ProviderRef: ref, CallPhone: "70000000000", CallPhonePretty: "+7 (000) 000-0000"}, nil
}

// Status — подтверждено, если прошло confirmAfter; неизвестная проверка — истекла.
func (s *Sandbox) Status(_ context.Context, ref string) (buyer.CallStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	started, ok := s.started[ref]
	switch {
	case !ok:
		return buyer.CallExpired, nil
	case s.now().Sub(started) >= s.confirmAfter:
		return buyer.CallConfirmed, nil
	default:
		return buyer.CallPending, nil
	}
}
