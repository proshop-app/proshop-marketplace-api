package reference

import (
	"context"
	"strconv"

	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
)

// Apply применяет поставку одной транзакцией: новые записи заводятся,
// существующим добавляются недостающие варианты, кандидат подтверждается,
// если поставка его подтверждает. Отклонённое и выведенное оператором не
// трогается, свободные поля существующей записи не перезаписываются.
// Ключ другой записи — конфликт всей поставки.
func (s *Service) Apply(ctx context.Context, d reference.Delivery) (reference.DeliveryResult, error) {
	result := reference.DeliveryResult{Dictionary: d.Dictionary, Release: d.Version}
	if err := reference.ValidateDelivery(d); err != nil {
		return result, err
	}
	meta := reference.Meta{Actor: "delivery:" + strconv.FormatInt(d.Version, 10), Source: reference.SourceSeed}
	revision, err := s.mutate(ctx, d.Dictionary, func(ctx context.Context, m *session) error {
		latest, err := m.repo.LatestDelivery(ctx, d.Dictionary)
		if err != nil {
			return err
		}
		apply, err := reference.PlanDelivery(latest, d)
		if err != nil || !apply {
			return err
		}
		from := m.dict.Revision()
		for _, entry := range d.Entries {
			if err := s.deliver(ctx, m, entry, meta, &result); err != nil {
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
	})
	result.Revision = revision
	return result, err
}

// Rollback откатывает последнюю поставку обратными образами её ревизий, пока
// после неё в словаре не было правок. Повтор отката ничего не меняет.
func (s *Service) Rollback(ctx context.Context, d reference.Delivery) (reference.DeliveryResult, error) {
	result := reference.DeliveryResult{Dictionary: d.Dictionary, Release: d.Version, RolledBack: true}
	if err := reference.ValidateDelivery(d); err != nil {
		return result, err
	}
	meta := reference.Meta{Actor: "rollback:" + strconv.FormatInt(d.Version, 10), Source: reference.SourceSeed}
	revision, err := s.mutate(ctx, d.Dictionary, func(ctx context.Context, m *session) error {
		latest, err := m.repo.LatestDelivery(ctx, d.Dictionary)
		if err != nil {
			return err
		}
		apply, err := reference.PlanDeliveryRollback(latest, m.dict, d)
		if err != nil || !apply {
			return err
		}
		changes, err := m.repo.Changes(ctx, d.Dictionary, latest.RevisionFrom, latest.RevisionTo)
		if err != nil {
			return err
		}
		for i := len(changes) - 1; i >= 0; i-- {
			if err := s.revert(ctx, m, changes[i], meta); err != nil {
				return err
			}
			result.Updated++
		}
		result.Changed = true
		return m.repo.MarkDeliveryRollback(ctx, d.Dictionary, d.Version, m.dict.Revision())
	})
	result.Revision = revision
	return result, err
}

func (s *Service) deliver(
	ctx context.Context,
	m *session,
	item reference.DeliveryEntry,
	meta reference.Meta,
	result *reference.DeliveryResult,
) error {
	_, key, err := m.dict.Spelling(item.Canonical)
	if err != nil {
		return err
	}
	owners, err := m.repo.KeyOwners(ctx, m.dict.Code(), []string{key})
	if err != nil {
		return err
	}
	draft := reference.NewDraft(m.dict.Code())
	owner, exists := owners[key]
	if !exists {
		input := reference.NewEntryInput{
			ID:               s.ids.NewUUID(),
			CanonicalAliasID: s.ids.NewUUID(),
			Name:             item.Canonical,
			Status:           item.Status,
			Source:           meta.Source,
			Metadata:         item.Metadata,
			Aliases:          s.deliveryAliases(item.Aliases),
		}
		entry, err := reference.NewEntry(m.dict, input, m.now)
		if err != nil {
			return err
		}
		draft.Track(entry)
		if _, err := m.record(ctx, draft, reference.ChangeCreated, entry.ID(), meta, 0); err != nil {
			return err
		}
		result.Created++
		return nil
	}
	entry, err := m.repo.Entry(ctx, m.dict.Code(), owner)
	if err != nil {
		return err
	}
	if !entry.Resolvable() {
		result.Skipped++
		return nil
	}
	draft.Track(entry)
	if err := s.extend(m, entry, item, meta); err != nil {
		return err
	}
	changed, err := m.record(ctx, draft, reference.ChangeUpdated, entry.ID(), meta, 0)
	if changed {
		result.Updated++
	}
	return err
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
	if len(item.Metadata) > 0 && string(entry.Metadata()) == "{}" {
		if _, err := entry.SetMetadata(item.Metadata); err != nil {
			return err
		}
	}
	return nil
}

// revert применяет обратный образ записи журнала новой ревизией. Запись,
// изменившаяся после неё, — конфликт: откат не затирает чужую правку.
func (s *Service) revert(ctx context.Context, m *session, change reference.Change, meta reference.Meta) error {
	draft := reference.NewDraft(m.dict.Code())
	for i, image := range change.Forward {
		current, err := m.repo.Entry(ctx, m.dict.Code(), image.ID)
		if err != nil && !isNotFound(err) {
			return err
		}
		var state *reference.EntryState
		if current != nil {
			copied := current.State()
			state = &copied
		}
		if !reference.SameContent(state, image.State) {
			return reference.ErrRevertConflict.WithMessage("запись " + string(image.ID) + " изменилась после ревизии " +
				strconv.FormatInt(change.Revision, 10))
		}
		target := change.Inverse[i].State
		switch {
		case target == nil && current != nil:
			draft.Delete(current)
		case target != nil && current == nil:
			draft.Track(reference.RevivedEntry(*target, m.now))
		case target != nil:
			draft.Track(current).Revert(*target)
		}
	}
	_, err := m.record(ctx, draft, reference.ChangeReverted, change.EntryID, meta, change.Revision)
	return err
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
