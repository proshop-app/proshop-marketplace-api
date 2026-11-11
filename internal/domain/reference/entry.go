// Package reference — реестр справочных сущностей: словари, записи со
// стабильным id, варианты написания и журнал ревизий. Бренд — первый
// открытый словарь; код не знает о нём ничего, кроме настроек словаря.
package reference

import (
	"bytes"
	"encoding/json"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Status — статус записи.
type Status string

// AliasKind — вид варианта написания.
type AliasKind string

// Source — откуда пришла запись или вариант.
type Source string

const (
	// StatusCandidate — запись встретилась, но не подтверждена; точное совпадение разрешается.
	StatusCandidate Status = "candidate"
	// StatusConfirmed — подтверждённая запись.
	StatusConfirmed Status = "confirmed"
	// StatusMerged — запись объединена и навсегда разрешается в MergedInto.
	StatusMerged Status = "merged"
	// StatusDeprecated — запись выведена из оборота; её ключи помнятся, совпадением не считаются.
	StatusDeprecated Status = "deprecated"
	// StatusRejected — кандидат отклонён; ключ помнится, повторный кандидат считается встречей.
	StatusRejected Status = "rejected"
)

// Виды вариантов написания.
const (
	AliasSpelling     AliasKind = "spelling"
	AliasTranslit     AliasKind = "translit"
	AliasAbbreviation AliasKind = "abbreviation"
	AliasFormer       AliasKind = "former"
	AliasLocale       AliasKind = "locale"
)

// Источники пополнения первого этапа.
const (
	SourceSeed     Source = "seed"
	SourceOperator Source = "operator"
	SourceCatalog  Source = "catalog"
)

const (
	maxMetadataBytes = 4096
	maxAliasWeight   = 100
)

var (
	localePattern = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})?$`)
	// transitions — переходы статуса, доступные методу ChangeStatus; объединение — только MergeInto.
	transitions = map[Status][]Status{
		StatusCandidate: {StatusConfirmed, StatusRejected},
		StatusConfirmed: {StatusDeprecated},
	}
)

// AliasState — вариант написания записи; ключ уникален в пределах словаря.
type AliasState struct {
	ID     ids.UUID  `json:"id"`
	Text   string    `json:"text"`
	Key    string    `json:"key"`
	Kind   AliasKind `json:"kind"`
	Locale string    `json:"locale,omitempty"`
	Source Source    `json:"source"`
	Weight int16     `json:"weight"`
}

// AliasInput — новый вариант написания.
type AliasInput struct {
	ID     ids.UUID
	Text   string
	Kind   AliasKind
	Locale string
	Weight int16
}

// EntryState — плоский снимок записи. Поля без JSON-имени не входят в образы
// журнала: версия, ревизия, счётчик встреч и время правки меняются при
// каждом сохранении и не описывают содержимое записи.
type EntryState struct {
	ID            ids.UUID        `json:"id"`
	Dictionary    string          `json:"dictionary"`
	CanonicalName string          `json:"canonicalName"`
	CanonicalKey  string          `json:"canonicalKey"`
	Status        Status          `json:"status"`
	MergedInto    ids.UUID        `json:"mergedInto,omitempty"`
	Source        Source          `json:"source"`
	Metadata      json.RawMessage `json:"metadata"`
	Aliases       []AliasState    `json:"aliases"`
	CreatedAt     time.Time       `json:"createdAt"`
	Occurrences   int64           `json:"-"`
	Revision      int64           `json:"-"`
	Version       int64           `json:"-"`
	UpdatedAt     time.Time       `json:"-"`
}

// NewEntryInput — новая запись: каноническое имя становится её первым вариантом.
type NewEntryInput struct {
	Occurrences      int64
	ID               ids.UUID
	CanonicalAliasID ids.UUID
	Name             string
	Status           Status
	Source           Source
	Metadata         json.RawMessage
	Aliases          []AliasInput
}

// Origin — состояние записи на момент чтения: по нему репозиторий строит
// условное обновление, а сохранение — разницу вариантов.
type Origin struct {
	Status  Status
	Version int64
	Aliases []AliasState
}

// Entry — запись словаря. Меняется только своими методами; методы сообщают,
// изменилось ли содержимое, повтор — не изменение.
type Entry struct {
	state  EntryState
	origin Origin
}

// NewEntry заводит запись кандидатом или подтверждённой. Каноническое имя
// хранится и как вариант: одна уникальность ключей на весь словарь.
func NewEntry(dict *Dictionary, input NewEntryInput, now time.Time) (*Entry, error) {
	if input.Status != StatusCandidate && input.Status != StatusConfirmed {
		return nil, ErrTransition.WithMessage("новая запись — кандидат или подтверждённая")
	}
	name, key, err := dict.CanonicalSpelling(input.Name)
	if err != nil {
		return nil, err
	}
	metadata, err := CleanMetadata(input.Metadata)
	if err != nil {
		return nil, err
	}
	if input.Occurrences < 0 {
		return nil, ErrInvalid
	}
	if input.Occurrences == 0 {
		input.Occurrences = 1
	}
	now = moment(now)
	e := &Entry{state: EntryState{
		ID:            input.ID,
		Dictionary:    dict.Code(),
		CanonicalName: name,
		CanonicalKey:  key,
		Status:        input.Status,
		Source:        input.Source,
		Metadata:      metadata,
		Aliases: []AliasState{{
			ID:     input.CanonicalAliasID,
			Text:   name,
			Key:    key,
			Kind:   AliasSpelling,
			Source: input.Source,
		}},
		CreatedAt:   now,
		UpdatedAt:   now,
		Occurrences: input.Occurrences,
	}}
	for _, alias := range input.Aliases {
		if _, _, err := e.AddAlias(dict, alias, input.Source, now); err != nil {
			return nil, err
		}
	}
	return e, nil
}

// RestoreEntry поднимает запись из хранилища. Свободные поля приводятся к
// каноническому виду: jsonb переставляет ключи, а образ журнала сравнивается побайтно.
func RestoreEntry(state EntryState) *Entry {
	state = cloneState(state)
	if metadata, err := canonicalJSON(state.Metadata); err == nil {
		state.Metadata = metadata
	}
	return &Entry{state: state, origin: Origin{
		Status:  state.Status,
		Version: state.Version,
		Aliases: slices.Clone(state.Aliases),
	}}
}

// RevivedEntry — запись, удалённая ревизией, восстанавливается из её образа как новая строка.
func RevivedEntry(image EntryState, now time.Time) *Entry {
	state := cloneState(image)
	state.Version = 0
	state.UpdatedAt = moment(now)
	return &Entry{state: state}
}

// CleanMetadata проверяет свободные поля записи: объект не больше 4 КиБ;
// metadata.directions — подсказка направлений, только массив строк, ничего не запрещает.
func CleanMetadata(raw json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage("{}"), nil
	}
	if len(raw) > maxMetadataBytes {
		return nil, ErrLimit.WithMessage("metadata больше 4 КиБ")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, ErrInvalid.WithMessage("metadata — JSON-объект")
	}
	if directions, ok := fields["directions"]; ok {
		var codes []string
		if err := json.Unmarshal(directions, &codes); err != nil || codes == nil {
			return nil, ErrInvalid.WithMessage("metadata.directions — массив строк")
		}
	}
	return canonicalJSON(raw)
}

// State — снимок записи; варианты упорядочены по ключу.
func (e *Entry) State() EntryState { return cloneState(e.state) }

// Origin — исходное состояние для условного обновления.
func (e *Entry) Origin() Origin {
	origin := e.origin
	origin.Aliases = slices.Clone(e.origin.Aliases)
	return origin
}

// IsNew — записи ещё нет в хранилище.
func (e *Entry) IsNew() bool { return e.origin.Version == 0 }

// MarkSaved — сохранённое состояние становится исходным.
func (e *Entry) MarkSaved() {
	e.origin = Origin{Status: e.state.Status, Version: e.state.Version, Aliases: slices.Clone(e.state.Aliases)}
}

// ID — идентификатор записи.
func (e *Entry) ID() ids.UUID { return e.state.ID }

// Status — статус записи.
func (e *Entry) Status() Status { return e.state.Status }

// MergedInto — запись, в которую объединена эта; пусто — не объединена.
func (e *Entry) MergedInto() ids.UUID { return e.state.MergedInto }

// Revision — ревизия журнала, которая последней меняла запись; по ней If-Match.
func (e *Entry) Revision() int64 { return e.state.Revision }

// Metadata — свободные поля записи.
func (e *Entry) Metadata() json.RawMessage { return slices.Clone(e.state.Metadata) }

// Keys — ключи всех вариантов записи.
func (e *Entry) Keys() []string {
	keys := make([]string, 0, len(e.state.Aliases))
	for _, alias := range e.state.Aliases {
		keys = append(keys, alias.Key)
	}
	return keys
}

// Alias — вариант записи по ключу.
func (e *Entry) Alias(key string) (AliasState, bool) {
	for _, alias := range e.state.Aliases {
		if alias.Key == key {
			return alias, true
		}
	}
	return AliasState{}, false
}

// Resolvable — точное совпадение с ключом записи считается совпадением.
func (e *Entry) Resolvable() bool {
	return e.state.Status == StatusCandidate || e.state.Status == StatusConfirmed
}

// Touch отмечает сохранение ревизией журнала: версия растёт на единицу за сохранение.
func (e *Entry) Touch(revision int64, now time.Time) {
	e.state.Revision = revision
	e.Bump(now)
}

// Bump — сохранение без ревизии журнала, например счётчик встреч.
func (e *Entry) Bump(now time.Time) {
	e.state.Version = e.origin.Version + 1
	e.state.UpdatedAt = moment(now)
}

// Meet считает повторную встречу ключа: кандидат и отклонённый ключ не
// заводятся заново, очередь кандидатов ранжируется этим счётчиком.
func (e *Entry) Meet(now time.Time) {
	e.state.Occurrences++
	e.Bump(now)
}

// Rename меняет каноническое имя. Новый ключ становится вариантом записи,
// прежний вариант остаётся: старое написание продолжает разрешаться.
func (e *Entry) Rename(dict *Dictionary, name string, aliasID ids.UUID, now time.Time) (bool, error) {
	if err := e.editable(); err != nil {
		return false, err
	}
	clean, key, err := dict.CanonicalSpelling(name)
	if err != nil {
		return false, err
	}
	if clean == e.state.CanonicalName {
		return false, nil
	}
	if _, ok := e.Alias(key); !ok {
		spelling := AliasInput{ID: aliasID, Text: clean, Kind: AliasSpelling}
		if _, _, err := e.AddAlias(dict, spelling, SourceOperator, now); err != nil {
			return false, err
		}
	}
	e.state.CanonicalName = clean
	e.state.CanonicalKey = key
	return true, nil
}

// ChangeStatus — переход статуса: кандидат подтверждается или отклоняется,
// подтверждённая запись выводится из оборота. Повтор статуса — не изменение.
func (e *Entry) ChangeStatus(target Status, _ time.Time) (bool, error) {
	if e.state.Status == target {
		return false, nil
	}
	if !slices.Contains(transitions[e.state.Status], target) {
		message := "переход " + string(e.state.Status) + " → " + string(target) + " недопустим"
		return false, ErrTransition.WithMessage(message)
	}
	e.state.Status = target
	return true, nil
}

// SetMetadata заменяет свободные поля записи целиком.
func (e *Entry) SetMetadata(raw json.RawMessage) (bool, error) {
	metadata, err := CleanMetadata(raw)
	if err != nil {
		return false, err
	}
	if bytes.Equal(metadata, e.state.Metadata) {
		return false, nil
	}
	e.state.Metadata = metadata
	return true, nil
}

// AddAlias добавляет вариант. Тот же ключ у этой записи — повтор, вариант не
// меняется; занятость ключа другой записью проверяет сценарий по хранилищу.
func (e *Entry) AddAlias(dict *Dictionary, input AliasInput, source Source, _ time.Time) (AliasState, bool, error) {
	if err := e.editable(); err != nil {
		return AliasState{}, false, err
	}
	text, key, err := dict.Spelling(input.Text)
	if err != nil {
		return AliasState{}, false, err
	}
	if existing, ok := e.Alias(key); ok {
		return existing, false, nil
	}
	kind := input.Kind
	if kind == "" {
		kind = AliasSpelling
	}
	if !dict.AllowsKind(kind) {
		return AliasState{}, false, ErrInvalid.WithMessage("словарь не принимает вариант вида " + string(kind))
	}
	if input.Locale != "" && !localePattern.MatchString(input.Locale) {
		return AliasState{}, false, ErrInvalid.WithMessage("locale — код языка BCP 47")
	}
	if input.Weight < 0 || input.Weight > maxAliasWeight {
		return AliasState{}, false, ErrInvalid.WithMessage("вес варианта — от 0 до 100")
	}
	if len(e.state.Aliases) >= dict.Limits().MaxAliases {
		return AliasState{}, false, ErrLimit.WithMessage(
			"у записи не больше " + strconv.Itoa(dict.Limits().MaxAliases) + " вариантов",
		)
	}
	alias := AliasState{
		ID:     input.ID,
		Text:   text,
		Key:    key,
		Kind:   kind,
		Locale: input.Locale,
		Source: source,
		Weight: input.Weight,
	}
	e.state.Aliases = sortedAliases(append(e.state.Aliases, alias))
	return alias, true, nil
}

// RemoveAlias удаляет вариант. Вариант канонического имени удаляется только
// переименованием: иначе запись перестала бы разрешаться своим именем.
func (e *Entry) RemoveAlias(id ids.UUID, _ time.Time) (AliasState, error) {
	if err := e.editable(); err != nil {
		return AliasState{}, err
	}
	for i, alias := range e.state.Aliases {
		if alias.ID != id {
			continue
		}
		if alias.Key == e.state.CanonicalKey {
			return AliasState{}, ErrCanonicalAlias
		}
		e.state.Aliases = slices.Delete(slices.Clone(e.state.Aliases), i, i+1)
		return alias, nil
	}
	return AliasState{}, ErrAliasNotFound
}

// MergeInto объединяет запись в target: варианты переходят к выжившей записи
// с теми же id, эта запись получает merged и навсегда разрешается в target.
func (e *Entry) MergeInto(dict *Dictionary, target *Entry, _ time.Time) (bool, error) {
	if e.state.Status == StatusMerged && e.state.MergedInto == target.ID() {
		return false, nil
	}
	if e.ID() == target.ID() || e.state.Status != StatusConfirmed || target.state.Status != StatusConfirmed {
		return false, ErrMergeTarget
	}
	if len(e.state.Aliases)+len(target.state.Aliases) > dict.Limits().MaxAliases {
		return false, ErrLimit.WithMessage(
			"после объединения у записи больше " + strconv.Itoa(dict.Limits().MaxAliases) + " вариантов",
		)
	}
	target.state.Aliases = sortedAliases(append(target.state.Aliases, e.state.Aliases...))
	e.state.Aliases = []AliasState{}
	e.state.Status = StatusMerged
	e.state.MergedInto = target.ID()
	return true, nil
}

// Repoint переносит объединённую запись на нового выжившего: цепочки
// схлопываются при записи, и глубина разрешения не растёт.
func (e *Entry) Repoint(target ids.UUID) bool {
	if e.state.Status != StatusMerged || e.state.MergedInto == target {
		return false
	}
	e.state.MergedInto = target
	return true
}

// Revert возвращает записи содержимое образа журнала; версия продолжает расти.
func (e *Entry) Revert(image EntryState) bool {
	if SameContent(&e.state, &image) {
		return false
	}
	restored := cloneState(image)
	restored.Occurrences = e.state.Occurrences
	restored.Revision = e.state.Revision
	restored.Version = e.state.Version
	restored.UpdatedAt = e.state.UpdatedAt
	e.state = restored
	return true
}

// SameContent сравнивает содержимое записей без версии, ревизии и времени:
// по нему откат проверяет, что запись не менялась после откатываемой ревизии.
func SameContent(a, b *EntryState) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	left, right := content(*a), content(*b)
	return bytes.Equal(left, right)
}

// canonicalJSON — JSON с ключами по алфавиту и без пробелов; числа не теряют точность.
func canonicalJSON(raw json.RawMessage) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, ErrInvalid.WithMessage("metadata — JSON-объект")
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, ErrInvalid.WithMessage("metadata — JSON-объект")
	}
	return canonical, nil
}

func (e *Entry) editable() error {
	if e.state.Status == StatusCandidate || e.state.Status == StatusConfirmed {
		return nil
	}
	return ErrTransition.WithMessage("запись в статусе " + string(e.state.Status) + " не меняется")
}

func content(state EntryState) []byte {
	state = cloneState(state)
	state.CreatedAt = time.Time{}
	raw, _ := json.Marshal(state)
	return raw
}

func cloneState(state EntryState) EntryState {
	state.Aliases = sortedAliases(slices.Clone(state.Aliases))
	if state.Aliases == nil {
		state.Aliases = []AliasState{}
	}
	if len(state.Metadata) == 0 {
		state.Metadata = json.RawMessage("{}")
	} else {
		state.Metadata = slices.Clone(state.Metadata)
	}
	return state
}

func sortedAliases(aliases []AliasState) []AliasState {
	sort.Slice(aliases, func(i, j int) bool { return aliases[i].Key < aliases[j].Key })
	return aliases
}

// moment — время с точностью базы: образ из журнала и строка из базы совпадают.
func moment(t time.Time) time.Time {
	return t.UTC().Truncate(time.Microsecond)
}
