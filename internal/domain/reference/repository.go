package reference

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// ListFilter — страница записей словаря; пустой Status — все статусы.
type ListFilter struct {
	Status Status
	Limit  int
	Offset int
}

// Candidate — нечёткое совпадение для resolve: никогда не привязывается само.
type Candidate struct {
	EntryID   ids.UUID
	Canonical string
	AliasKey  string
	Status    Status
	// Prefix — ключ варианта начинается с ключа запроса; такие идут первыми.
	Prefix     bool
	Similarity float64
}

// Presence — присутствие записи в узле дерева площадки по опубликованным
// предложениям. Влияет только на порядок кандидатов и навигацию, не на разрешение.
type Presence struct {
	CategoryID string
	Offers     int
	Sellers    int
}

// Repository — реестр в одной транзакции. Мутация словаря начинается с
// LockDictionary: изменения одного словаря идут по одной, поэтому его
// ревизии видны потребителю в порядке фиксации и дельта since ничего не теряет.
type Repository interface {
	// CreateDictionary не меняет существующие настройки; true — заголовок создан этой транзакцией.
	CreateDictionary(ctx context.Context, state DictionaryState) (bool, error)
	LockDictionary(ctx context.Context, code string) (*Dictionary, error)
	Dictionary(ctx context.Context, code string) (*Dictionary, error)
	Dictionaries(ctx context.Context) ([]*Dictionary, error)
	SaveDictionary(ctx context.Context, dict *Dictionary) error

	// Append пишет запись журнала первой в изменении и возвращает её ревизию.
	Append(ctx context.Context, change Change) (int64, error)
	// Changes — записи журнала словаря в (from, to] по возрастанию ревизии.
	Changes(ctx context.Context, dictionary string, from, to int64) ([]Change, error)
	// ChangedEntries — записи, затронутые ревизиями (since, until].
	ChangedEntries(ctx context.Context, dictionary string, since, until int64) ([]ids.UUID, error)
	// Horizon — последняя ревизия словаря старше before; ноль — таких нет.
	Horizon(ctx context.Context, dictionary string, before time.Time) (int64, error)

	Entry(ctx context.Context, dictionary string, id ids.UUID) (*Entry, error)
	Entries(ctx context.Context, dictionary string, entries []ids.UUID) ([]*Entry, error)
	AllEntries(ctx context.Context, dictionary string) ([]*Entry, error)
	List(ctx context.Context, dictionary string, filter ListFilter) ([]*Entry, int, error)
	// MergedInto — записи, объединённые в id: при новом объединении они переносятся дальше.
	MergedInto(ctx context.Context, dictionary string, id ids.UUID) ([]*Entry, error)
	// EntryByAlias — запись, которой принадлежит вариант.
	EntryByAlias(ctx context.Context, dictionary string, alias ids.UUID) (*Entry, error)
	// KeyOwners — записи, которым принадлежат ключи; свободных ключей в ответе нет.
	KeyOwners(ctx context.Context, dictionary string, keys []string) (map[string]ids.UUID, error)
	// Candidates — записи по префиксу ключа и триграммам, кроме отклонённых и выведенных.
	Candidates(ctx context.Context, dictionary, key string, limit int) ([]Candidate, error)
	CountStatus(ctx context.Context, dictionary string, status Status) (int64, error)
	// Save пишет изменённые записи условно по исходной версии и удаляет отмеченные.
	Save(ctx context.Context, saved, deleted []*Entry) error

	LatestDelivery(ctx context.Context, dictionary string) (*DeliveryRecord, error)
	RecordDelivery(ctx context.Context, record DeliveryRecord) error
	MarkDeliveryRollback(ctx context.Context, dictionary string, version, revision int64) error

	// ReplacePresence заменяет присутствие записи целиком: пересчёт идемпотентен.
	ReplacePresence(ctx context.Context, entry ids.UUID, rows []Presence, at time.Time) error
}
