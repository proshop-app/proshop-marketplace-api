package postgres

import (
	"sync"

	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

// Snapshots хранит последние зафиксированные неизменяемые снимки одной базы.
// Сборка процесса передаёт один объект всем UnitOfWork; нулевое значение готово к работе.
type Snapshots struct {
	taxonomy   snapshot[taxonomy.Definition]
	moderation snapshot[moderation.Rules]
}

type snapshot[T any] struct {
	mu      sync.RWMutex
	value   T
	version int64
	valid   bool
}

// snapshotReader переиспользуется репозиториями одной транзакции, включая чтение своих записей.
type snapshotReader[T any] struct {
	shared      *snapshot[T]
	afterCommit func(func())
	value       T
	version     int64
	valid       bool
}

func (s *snapshot[T]) get(version int64) (T, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.value, s.valid && s.version == version
}

func (s *snapshot[T]) put(version int64, value T) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Запоздавший AfterCommit не вытесняет более новую зафиксированную версию.
	if !s.valid || version > s.version {
		s.version, s.value, s.valid = version, value, true
	}
}

func (r *snapshotReader[T]) current(version int64, load func() (T, error)) (T, error) {
	if r.valid && r.version == version {
		return r.value, nil
	}
	if value, ok := r.shared.get(version); ok {
		r.version, r.value, r.valid = version, value, true
		return value, nil
	}
	value, err := load()
	if err != nil {
		return value, err
	}
	r.version, r.value, r.valid = version, value, true
	// До успешного COMMIT снимок виден только этой транзакции. Откат, паника
	// или ошибка COMMIT не публикуют прочитанную собственную незавершённую запись.
	r.afterCommit(func() { r.shared.put(version, value) })

	return value, nil
}
