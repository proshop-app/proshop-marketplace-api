package ports

import "time"

// Виды подсказок для фоновой работы вне outbox.
const (
	TaxonomyWork          = "taxonomy.revalidation"
	TaxonomyDisplayWork   = "taxonomy.display"
	TaxonomySelectionWork = "taxonomy.selection"
	IndexMaintenanceWork  = "index.maintenance"
)

// WorkHints хранит ближайший известный next_at каждого вида. Нулевой срок
// просит сверить вид с базой, пустой вид — сверить всю очередь.
type WorkHints map[string]time.Time

// WorkSignal сообщает об изменении очереди после фиксации.
type WorkSignal interface {
	Notify(WorkHints)
}

// WorkWatch копит изменения до следующего прохода.
type WorkWatch interface {
	Changed() <-chan struct{}
	Take() WorkHints
}

// Add сохраняет ранний срок. Если голова могла смениться, срок нужно перечитать.
func (h WorkHints) Add(kind string, next time.Time) {
	if previous, ok := h[kind]; !ok || next.IsZero() || (!previous.IsZero() && next.Before(previous)) {
		h[kind] = next
	}
}
