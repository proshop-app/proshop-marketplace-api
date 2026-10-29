package reference

import (
	"slices"
	"sort"
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// ChangeKind — вид записи журнала.
type ChangeKind string

// EventKind — что изменение значит для потребителя, который держит ссылки на записи.
type EventKind string

const (
	// ChangeCreated — заведена запись.
	ChangeCreated ChangeKind = "entry.created"
	// ChangeUpdated — переименование, статус или свободные поля.
	ChangeUpdated ChangeKind = "entry.updated"
	// ChangeAliasAdded — добавлен вариант написания.
	ChangeAliasAdded ChangeKind = "alias.added"
	// ChangeAliasRemoved — удалён вариант написания.
	ChangeAliasRemoved ChangeKind = "alias.removed"
	// ChangeMerged — запись объединена в другую.
	ChangeMerged ChangeKind = "entry.merged"
	// ChangeReverted — применён обратный набор другой ревизии.
	ChangeReverted ChangeKind = "change.reverted"
)

// Виды событий из контракта catalog.reference.changed.
const (
	EventAliasAdded    EventKind = "alias-added"
	EventAliasRemoved  EventKind = "alias-removed"
	EventRenamed       EventKind = "renamed"
	EventMerged        EventKind = "merged"
	EventStatusChanged EventKind = "status-changed"
)

// eventOrder — порядок видов в событии: стабильный, чтобы повтор давал то же тело.
var eventOrder = []EventKind{
	EventAliasAdded,
	EventAliasRemoved,
	EventRenamed,
	EventMerged,
	EventStatusChanged,
}

// Image — содержимое записи в журнале; State nil — записи нет.
type Image struct {
	ID    ids.UUID    `json:"id"`
	State *EntryState `json:"state"`
}

// Meta — кто и почему меняет реестр.
type Meta struct {
	Actor  string
	Source Source
	Reason string
}

// Change — запись журнала только на добавление: прямой и обратный образы
// всех затронутых записей. Откат — обратный образ новой ревизией.
type Change struct {
	Revision   int64
	Dictionary string
	EntryID    ids.UUID
	EntryIDs   []ids.UUID
	Kind       ChangeKind
	Meta       Meta
	// Reverts — ревизия, обратный образ которой применён; ноль — обычное изменение.
	Reverts int64
	Forward []Image
	Inverse []Image
	At      time.Time
}

// Effects — следствия изменений для события: виды, записи и затронутые ключи.
type Effects struct {
	Kinds   []EventKind
	Entries []ids.UUID
	Keys    []string
}

// Draft собирает одно изменение словаря: исходные образы записей при первом
// касании и их состояние после методов агрегата.
type Draft struct {
	dictionary string
	order      []ids.UUID
	entries    map[ids.UUID]*Entry
	before     map[ids.UUID]*EntryState
	deleted    map[ids.UUID]bool
}

// NewDraft — пустое изменение словаря.
func NewDraft(dictionary string) *Draft {
	return &Draft{
		dictionary: dictionary,
		entries:    map[ids.UUID]*Entry{},
		before:     map[ids.UUID]*EntryState{},
		deleted:    map[ids.UUID]bool{},
	}
}

// Track запоминает исходный образ записи до первого изменения; новая запись образа не имеет.
func (d *Draft) Track(e *Entry) *Entry {
	if existing, ok := d.entries[e.ID()]; ok {
		return existing
	}
	d.order = append(d.order, e.ID())
	d.entries[e.ID()] = e
	if !e.IsNew() {
		state := e.State()
		d.before[e.ID()] = &state
	}
	return e
}

// Tracked — запись, уже взятая в изменение; иначе nil.
func (d *Draft) Tracked(id ids.UUID) *Entry {
	return d.entries[id]
}

// Delete отмечает удаление записи: так откат убирает запись, заведённую поставкой.
func (d *Draft) Delete(e *Entry) {
	d.Track(e)
	d.deleted[e.ID()] = true
}

// Change — запись журнала из образов записей, чьё содержимое изменилось;
// false — изменения нет, повтор не пишет ревизию.
func (d *Draft) Change(kind ChangeKind, primary ids.UUID, meta Meta, at time.Time) (Change, bool) {
	change := Change{
		Dictionary: d.dictionary,
		EntryID:    primary,
		Kind:       kind,
		Meta:       meta,
		At:         moment(at),
	}
	for _, id := range d.order {
		before, after := d.before[id], d.after(id)
		if SameContent(before, after) {
			continue
		}
		change.EntryIDs = append(change.EntryIDs, id)
		change.Forward = append(change.Forward, Image{ID: id, State: after})
		change.Inverse = append(change.Inverse, Image{ID: id, State: before})
	}
	return change, len(change.EntryIDs) > 0
}

// Saved — записи, которые нужно сохранить: изменённые и не удалённые.
func (d *Draft) Saved() []*Entry {
	var out []*Entry
	for _, id := range d.order {
		if !d.deleted[id] && !SameContent(d.before[id], d.after(id)) {
			out = append(out, d.entries[id])
		}
	}
	return out
}

// Deleted — записи, которые нужно удалить.
func (d *Draft) Deleted() []*Entry {
	var out []*Entry
	for _, id := range d.order {
		if d.deleted[id] && d.before[id] != nil {
			out = append(out, d.entries[id])
		}
	}
	return out
}

// EntriesDelta — на сколько изменение меняет число записей словаря.
func (d *Draft) EntriesDelta() int64 {
	var delta int64
	for _, id := range d.order {
		switch before, after := d.before[id], d.after(id); {
		case before == nil && after != nil:
			delta++
		case before != nil && after == nil:
			delta--
		}
	}
	return delta
}

// ClaimedKeys — ключи, которые запись получает в этом изменении, с её id.
// Их занятость проверяется по хранилищу; ключ, который отдаёт другая запись
// того же изменения (объединение), свободен.
func (d *Draft) ClaimedKeys() map[string]ids.UUID {
	claimed := map[string]ids.UUID{}
	for _, id := range d.order {
		after := d.after(id)
		if after == nil {
			continue
		}
		had := keySet(d.before[id])
		for _, alias := range after.Aliases {
			if !had[alias.Key] {
				claimed[alias.Key] = id
			}
		}
	}
	return claimed
}

// Releases — ключ отдаёт одна из записей изменения: он был у неё и пропал.
func (d *Draft) Releases(key string, owner ids.UUID) bool {
	if _, ok := d.entries[owner]; !ok {
		return false
	}
	return keySet(d.before[owner])[key] && !keySet(d.after(owner))[key]
}

// Effects — следствия записи журнала для потребителей ссылок.
func (c Change) Effects() Effects {
	kinds := map[EventKind]bool{}
	keys := map[string]bool{}
	for i := range c.Forward {
		before, after := c.Inverse[i].State, c.Forward[i].State
		had, has := keySet(before), keySet(after)
		for key := range has {
			if !had[key] {
				kinds[EventAliasAdded] = true
				keys[key] = true
			}
		}
		for key := range had {
			if !has[key] {
				kinds[EventAliasRemoved] = true
				keys[key] = true
			}
		}
		if before == nil || after == nil {
			continue
		}
		if before.CanonicalName != after.CanonicalName {
			kinds[EventRenamed] = true
			keys[before.CanonicalKey] = true
			keys[after.CanonicalKey] = true
		}
		switch {
		case before.MergedInto != after.MergedInto:
			kinds[EventMerged] = true
		case before.Status != after.Status:
			kinds[EventStatusChanged] = true
			// разрешение всех ключей записи меняется вместе со статусом
			for key := range has {
				keys[key] = true
			}
		}
	}
	effects := Effects{Entries: slices.Clone(c.EntryIDs)}
	for _, kind := range eventOrder {
		if kinds[kind] {
			effects.Kinds = append(effects.Kinds, kind)
		}
	}
	for key := range keys {
		effects.Keys = append(effects.Keys, key)
	}
	sort.Strings(effects.Keys)
	return effects
}

// Join складывает следствия нескольких записей журнала без повторов.
func (e Effects) Join(other Effects) Effects {
	kinds := map[EventKind]bool{}
	for _, kind := range append(slices.Clone(e.Kinds), other.Kinds...) {
		kinds[kind] = true
	}
	joined := Effects{}
	for _, kind := range eventOrder {
		if kinds[kind] {
			joined.Kinds = append(joined.Kinds, kind)
		}
	}
	seen := map[ids.UUID]bool{}
	for _, id := range append(slices.Clone(e.Entries), other.Entries...) {
		if !seen[id] {
			seen[id] = true
			joined.Entries = append(joined.Entries, id)
		}
	}
	keys := map[string]bool{}
	for _, key := range append(slices.Clone(e.Keys), other.Keys...) {
		keys[key] = true
	}
	for key := range keys {
		joined.Keys = append(joined.Keys, key)
	}
	sort.Strings(joined.Keys)
	return joined
}

func (d *Draft) after(id ids.UUID) *EntryState {
	if d.deleted[id] {
		return nil
	}
	state := d.entries[id].State()
	return &state
}

func keySet(state *EntryState) map[string]bool {
	set := map[string]bool{}
	if state == nil {
		return set
	}
	for _, alias := range state.Aliases {
		set[alias.Key] = true
	}
	return set
}
