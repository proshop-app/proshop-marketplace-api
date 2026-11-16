package reference

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// deliveryBatch — записей поставки на одну ревизию журнала: владельцы ключей,
// записи и сохранение — по одному обращению к хранилищу на пачку, а не на запись.
const deliveryBatch = 500

// applyDelivery применяет выпуск одной транзакцией пачками по deliveryBatch:
// новые записи заводятся, существующим добавляются недостающие варианты,
// кандидат подтверждается, если поставка его подтверждает. Отклонённое и
// выведенное оператором не трогается, свободные поля существующей записи не
// перезаписываются. Ключ другой записи — конфликт всей поставки.

func (s *Service) applyDelivery(
	ctx context.Context,
	m *session,
	d reference.Delivery,
	settings reference.DictionarySettings,
	meta reference.Meta,
	result *reference.DeliveryResult,
) error {
	latest, err := m.repo.LatestDelivery(ctx, d.Dictionary)
	if err != nil {
		return err
	}
	apply, err := reference.PlanDelivery(latest, d)
	if err != nil || !apply {
		return err
	}
	changed, err := m.dict.Configure(settings)
	if err != nil {
		return err
	}
	m.dirty = m.dirty || changed
	from := m.dict.Revision()
	for start := 0; start < len(d.Entries); start += deliveryBatch {
		batch := d.Entries[start:min(start+deliveryBatch, len(d.Entries))]
		if err := s.deliver(ctx, m, batch, meta, result); err != nil {
			return err
		}
	}
	result.Changed = true
	return m.repo.RecordDelivery(ctx, reference.DeliveryRecord{
		Dictionary:   d.Dictionary,
		Version:      d.Version,
		Checksum:     d.Checksum,
		RevisionFrom: from,
		RevisionTo:   m.dict.Revision(),
		AppliedAt:    m.now,
	})
}

// deliver применяет пачку поставки одной ревизией журнала.
func (s *Service) deliver(
	ctx context.Context,
	m *session,
	items []reference.DeliveryEntry,
	meta reference.Meta,
	result *reference.DeliveryResult,
) error {
	keys := make([]string, 0, len(items))
	for _, item := range items {
		_, key, err := m.dict.CanonicalSpelling(item.Canonical)
		if err != nil {
			return err
		}
		keys = append(keys, key)
	}
	owners, err := m.repo.KeyOwners(ctx, m.dict.Code(), keys)
	if err != nil {
		return err
	}
	existing, err := s.owners(ctx, m, owners)
	if err != nil {
		return err
	}
	draft := reference.NewDraft(m.dict.Code())
	touched := make([]*reference.Entry, 0, len(items))
	created := map[ids.UUID]bool{}
	for i, item := range items {
		entry, ok := existing[owners[keys[i]]]
		if ok && !entry.Resolvable() {
			result.Skipped++
			continue
		}
		if ok {
			draft.Track(entry)
			if err := s.extend(m, entry, item, meta); err != nil {
				return err
			}
		} else {
			if entry, err = s.newDelivered(m, item, meta); err != nil {
				return err
			}
			created[entry.ID()] = true
			draft.Track(entry)
		}
		touched = append(touched, entry)
	}
	if len(touched) == 0 {
		return nil
	}
	for _, entry := range touched {
		switch {
		case created[entry.ID()]:
			result.Created++
		case draft.Changed(entry.ID()):
			result.Updated++
		}
	}
	_, err = m.record(ctx, draft, reference.ChangeDelivered, touched[0].ID(), meta)
	return err
}

// owners — существующие записи-владельцы ключей пачки одним чтением.
func (s *Service) owners(
	ctx context.Context,
	m *session,
	owners map[string]ids.UUID,
) (map[ids.UUID]*reference.Entry, error) {
	list := make([]ids.UUID, 0, len(owners))
	seen := map[ids.UUID]bool{}
	for _, id := range owners {
		if !seen[id] {
			seen[id] = true
			list = append(list, id)
		}
	}
	entries, err := m.repo.Entries(ctx, m.dict.Code(), list)
	if err != nil {
		return nil, err
	}
	out := make(map[ids.UUID]*reference.Entry, len(entries))
	for _, entry := range entries {
		out[entry.ID()] = entry
	}
	return out, nil
}

func (s *Service) newDelivered(
	m *session,
	item reference.DeliveryEntry,
	meta reference.Meta,
) (*reference.Entry, error) {
	input := reference.NewEntryInput{
		ID:               s.ids.NewUUID(),
		CanonicalAliasID: s.ids.NewUUID(),
		Name:             item.Canonical,
		Status:           item.Status,
		Source:           meta.Source,
		Metadata:         item.Metadata,
		Aliases:          s.deliveryAliases(item.Aliases),
	}
	return reference.NewEntry(m.dict, input, m.now)
}

// extend дополняет существующую запись данными поставки, не отнимая ничего у оператора.
func (s *Service) extend(m *session, entry *reference.Entry, item reference.DeliveryEntry, meta reference.Meta) error {
	if item.Status == reference.StatusConfirmed {
		if _, err := entry.ChangeStatus(reference.StatusConfirmed, m.now); err != nil {
			return err
		}
	}
	for _, alias := range s.deliveryAliases(item.Aliases) {
		if _, _, err := entry.AddAlias(m.dict, alias, meta.Source, m.now); err != nil {
			return err
		}
	}
	if _, err := entry.DeliveryMetadata(item.Metadata); err != nil {
		return err
	}
	return nil
}

func (s *Service) deliveryAliases(aliases []reference.DeliveryAlias) []reference.AliasInput {
	inputs := make([]reference.AliasInput, 0, len(aliases))
	for _, alias := range aliases {
		inputs = append(inputs, reference.AliasInput{
			ID:     s.ids.NewUUID(),
			Text:   alias.Text,
			Kind:   alias.Kind,
			Locale: alias.Locale,
		})
	}
	return inputs
}
