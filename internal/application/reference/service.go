// Package reference — сценарии реестра справочных сущностей: мутации записей
// одной транзакцией с журналом и событием, разрешение написаний, снимки
// для потребителей и версионные поставки.
package reference

import (
	"encoding/json"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	// SchemaVersion — версия контрактов снимка, разрешения, записей и события.
	SchemaVersion = 1
	// MaxResolveText — длина текста resolve в символах.
	MaxResolveText = 200
	// DefaultHorizon — горизонт журнала для дельты since; старше — полный снимок.
	DefaultHorizon = 30 * 24 * time.Hour
	// maxCandidates — нечётких кандидатов в ответе resolve.
	maxCandidates = 10
	// maxEventList — потолок entryIds и affectedKeys в событии; больше —
	// потребитель перечитывает дельту и считает сам.
	maxEventList = 1000
	// maxPage — записей на страницу списка.
	maxPage = 200
	// defaultPage — записей на страницу, если размер не задан.
	defaultPage = 50
)

// Config — имя топика события и горизонт журнала.
type Config struct {
	// Topic — полное имя топика catalog.reference.changed с префиксом установки.
	Topic   string
	Horizon time.Duration
}

// Service — сценарии реестра поверх транзакций, часов и идентификаторов.
type Service struct {
	uow       ports.UnitOfWork
	clock     ports.Clock
	ids       ports.IDs
	publisher ports.Publisher
	config    Config
}

// AliasSpec — вариант написания в команде.
type AliasSpec struct {
	Text   string
	Kind   reference.AliasKind
	Locale string
	Weight int16
}

// CreateCommand — новая запись: оператор заводит подтверждённую, каталог — кандидата.
type CreateCommand struct {
	Dictionary string
	Name       string
	Status     reference.Status
	Aliases    []AliasSpec
	Metadata   json.RawMessage
	Meta       reference.Meta
}

// UpdateCommand — правка записи по ревизии If-Match; nil-поля не меняются.
type UpdateCommand struct {
	Dictionary string
	ID         ids.UUID
	IfMatch    int64
	Name       *string
	Status     *reference.Status
	Metadata   json.RawMessage
	Meta       reference.Meta
}

// MergeCommand — объединение записи ID в Into по ревизии If-Match записи ID.
type MergeCommand struct {
	Dictionary string
	ID         ids.UUID
	Into       ids.UUID
	IfMatch    int64
	Meta       reference.Meta
}

// AddAliasCommand — новый вариант написания записи.
type AddAliasCommand struct {
	Dictionary string
	EntryID    ids.UUID
	Alias      AliasSpec
	Meta       reference.Meta
}

// RemoveAliasCommand — удаление варианта по его id.
type RemoveAliasCommand struct {
	Dictionary string
	AliasID    ids.UUID
	Meta       reference.Meta
}

// EntryResult — запись после сценария; Revision — голова словаря.
type EntryResult struct {
	Entry    reference.EntryState
	Alias    *reference.AliasState
	Revision int64
	// Created — запись заведена этим вызовом; повторный кандидат считается встречей.
	Created bool
	Changed bool
}

// EntryView — запись по id; объединённая разрешается в выжившую.
type EntryView struct {
	Entry    reference.EntryState
	Resolved *reference.EntryState
	Revision int64
}

// EntryPage — страница записей словаря.
type EntryPage struct {
	Entries  []reference.EntryState
	Total    int
	Revision int64
}

// Match — совпадение или кандидат resolve.
type Match struct {
	EntryID    ids.UUID
	Canonical  string
	AliasKey   string
	Status     reference.Status
	Confidence string
	Weight     float64
}

// Resolution — итог resolve: точное совпадение или кандидаты, никогда оба.
type Resolution struct {
	Dictionary string
	Key        string
	Normalizer string
	Revision   int64
	Match      *Match
	Candidates []Match
}

// Snapshot — словарь для потребителя: полный или дельта после since.
type Snapshot struct {
	Dictionary string
	Revision   int64
	Normalizer string
	Full       bool
	Entries    []reference.EntryState
	Removed    []ids.UUID
}

// Stats — ревизия и очередь кандидатов словаря для метрик.
type Stats struct {
	Dictionary string
	Revision   int64
	Entries    int64
	Candidates int64
}

// NewService собирает сценарии реестра. publisher нужен только разбору outbox.
func NewService(
	uow ports.UnitOfWork,
	clock ports.Clock,
	identities ports.IDs,
	publisher ports.Publisher,
	config Config,
) *Service {
	if config.Horizon <= 0 {
		config.Horizon = DefaultHorizon
	}
	return &Service{uow: uow, clock: clock, ids: identities, publisher: publisher, config: config}
}
