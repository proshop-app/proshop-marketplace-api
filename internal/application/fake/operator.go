package fake

import (
	"context"
	"sync"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/operator"
)

// OperatorSessions — сессии оператора в памяти с той же границей, что у Redis.
type OperatorSessions struct {
	mu       sync.Mutex
	sessions map[string]operator.Session
}

// Passwords — детерминированная замена дорогого Argon2 в сценариях.
type Passwords struct{}

// Replay — результат повторного разбора сообщения.
type Replay struct {
	Events []ports.ParkedEvent
	Err    error
}

// NewOperatorSessions — пустое хранилище.
func NewOperatorSessions() *OperatorSessions {
	return &OperatorSessions{sessions: map[string]operator.Session{}}
}

// Save сохраняет сессию.
func (s *OperatorSessions) Save(_ context.Context, session operator.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[session.TokenHash] = session
	return nil
}

// Get читает сессию по отпечатку.
func (s *OperatorSessions) Get(_ context.Context, hash string) (operator.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[hash]
	if !ok {
		return session, operator.ErrUnauthorized
	}
	return session, nil
}

// Delete отзывает сессию.
func (s *OperatorSessions) Delete(_ context.Context, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, hash)
	return nil
}

// Verify принимает только явно заданную пару тестовых данных.
func (Passwords) Verify(password, hash string) bool { return hash == "fake:"+password }

// Replay записывает переданный исходный конверт.
func (r *Replay) Replay(_ context.Context, event ports.ParkedEvent) error {
	r.Events = append(r.Events, event)
	return r.Err
}
