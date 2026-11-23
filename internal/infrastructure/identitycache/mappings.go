// Package identitycache хранит неизменяемые привязки одного issuer и клиента.
package identitycache

import (
	"context"
	"sync"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

type mapping struct {
	id        string
	expiresAt time.Time
}

// Mappings декорирует хранилище привязок одного issuer и клиента.
type Mappings struct {
	source   ports.IdentityMappings
	clock    ports.Clock
	mu       sync.RWMutex
	entries  map[string]mapping
	subjects []string
	next     int
}

// NewMappings оборачивает хранилище привязок одного issuer и клиента кэшем в памяти процесса: проверка access
// на каждом обращении иначе читала бы привязку subject → покупатель из базы. Ёмкость кэша выделяется сразу,
// чтобы вытеснение по кругу не перераспределяло память под нагрузкой.
func NewMappings(source ports.IdentityMappings, clock ports.Clock) *Mappings {
	return &Mappings{
		source:   source,
		clock:    clock,
		entries:  make(map[string]mapping),
		subjects: make([]string, 0, mappingLimit),
	}
}

// Bind записывает привязку прямо в хранилище и не кладёт её в кэш: привязка создаётся один раз при входе,
// а в кэш попадает первым же Find, уже подтверждённой хранилищем.
func (m *Mappings) Bind(ctx context.Context, subject, id string) error {
	return m.source.Bind(ctx, subject, id)
}

// Find отдаёт покупателя по subject из кэша, пока запись не старше mappingTTL, после чего перечитывает хранилище.
// Привязка не меняется, поэтому запись кэш не сбрасывает. Ошибка и пустой ответ не кэшируются.
func (m *Mappings) Find(ctx context.Context, subject string) (string, error) {
	m.mu.RLock()
	entry, ok := m.entries[subject]
	m.mu.RUnlock()
	if ok && m.clock.Now().Before(entry.expiresAt) {
		return entry.id, nil
	}

	id, err := m.source.Find(ctx, subject)
	if err == nil && id != "" {
		m.remember(subject, id)
	}

	return id, err
}

func (m *Mappings) remember(subject, id string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.clock.Now()
	if entry, exists := m.entries[subject]; !exists {
		m.insertSubject(subject)
	} else if now.Before(entry.expiresAt) {
		return
	}

	m.entries[subject] = mapping{
		id:        id,
		expiresAt: now.Add(mappingTTL),
	}
}

// Истёкшая привязка обновляется на своём месте, новая вытесняет запись по порядку вставки.
func (m *Mappings) insertSubject(subject string) {
	if len(m.subjects) < mappingLimit {
		m.subjects = append(m.subjects, subject)
		return
	}

	delete(m.entries, m.subjects[m.next])
	m.subjects[m.next] = subject
	m.next = (m.next + 1) % mappingLimit
}
