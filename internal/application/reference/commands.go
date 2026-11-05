package reference

import (
	"bytes"
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Create заводит запись. Кандидат с занятым ключом не заводится второй раз:
// встреча считается у владельца ключа, в том числе у отклонённой записи.
// Подтверждённая запись с занятым ключом — конфликт. Кандидат с вариантами
// или metadata на занятом ключе — тоже конфликт: встреча не склеивает данные,
// и присланные варианты не должны пропасть молча.
func (s *Service) Create(ctx context.Context, cmd CreateCommand) (EntryResult, error) {
	var result EntryResult
	revision, err := s.mutate(ctx, cmd.Dictionary, func(ctx context.Context, m *session) error {
		_, key, err := m.dict.Spelling(cmd.Name)
		if err != nil {
			return err
		}
		owners, err := m.repo.KeyOwners(ctx, m.dict.Code(), []string{key})
		if err != nil {
			return err
		}
		if owner, taken := owners[key]; taken {
			message := "написание «" + key + "» уже принадлежит записи " + string(owner)
			if cmd.Status != reference.StatusCandidate {
				return reference.ErrKeyTaken.WithMessage(message)
			}
			if len(cmd.Aliases) > 0 || len(bytes.TrimSpace(cmd.Metadata)) > 0 {
				message += ": варианты и metadata добавьте к этой записи"
				return reference.ErrKeyTaken.WithMessage(message)
			}
			return s.meet(ctx, m, owner, &result)
		}
		input := reference.NewEntryInput{
			Occurrences:      cmd.Occurrences,
			ID:               s.ids.NewUUID(),
			CanonicalAliasID: s.ids.NewUUID(),
			Name:             cmd.Name,
			Status:           cmd.Status,
			Source:           cmd.Meta.Source,
			Metadata:         cmd.Metadata,
			Aliases:          s.aliasInputs(cmd.Aliases),
		}
		entry, err := reference.NewEntry(m.dict, input, m.now)
		if err != nil {
			return err
		}
		draft := reference.NewDraft(m.dict.Code())
		draft.Track(entry)
		if _, err := m.record(ctx, draft, reference.ChangeCreated, entry.ID(), cmd.Meta, 0); err != nil {
			return err
		}
		result = EntryResult{Entry: entry.State(), Created: true, Changed: true}
		return nil
	})
	result.Revision = revision
	return result, err
}

// Update переименовывает запись, меняет статус или свободные поля по ревизии If-Match.
func (s *Service) Update(ctx context.Context, cmd UpdateCommand) (EntryResult, error) {
	var result EntryResult
	revision, err := s.mutate(ctx, cmd.Dictionary, func(ctx context.Context, m *session) error {
		entry, err := s.current(ctx, m, cmd.ID, cmd.IfMatch)
		if err != nil {
			return err
		}
		draft := reference.NewDraft(m.dict.Code())
		draft.Track(entry)
		if cmd.Name != nil {
			if _, err := entry.Rename(m.dict, *cmd.Name, s.ids.NewUUID(), m.now); err != nil {
				return err
			}
		}
		if cmd.Status != nil {
			if _, err := entry.ChangeStatus(*cmd.Status, m.now); err != nil {
				return err
			}
		}
		if cmd.Metadata != nil {
			if _, err := entry.SetMetadata(cmd.Metadata); err != nil {
				return err
			}
		}
		result.Changed, err = m.record(ctx, draft, reference.ChangeUpdated, entry.ID(), cmd.Meta, 0)
		result.Entry = entry.State()
		return err
	})
	result.Revision = revision
	return result, err
}

// Merge объединяет запись в выжившую. Записи, ранее объединённые в эту,
// переносятся на выжившую той же ревизией: цепочка не длиннее одного шага.
func (s *Service) Merge(ctx context.Context, cmd MergeCommand) (EntryResult, error) {
	var result EntryResult
	revision, err := s.mutate(ctx, cmd.Dictionary, func(ctx context.Context, m *session) error {
		source, err := s.current(ctx, m, cmd.ID, cmd.IfMatch)
		if err != nil {
			return err
		}
		target, err := s.survivor(ctx, m, cmd.Into)
		if err != nil {
			return err
		}
		draft := reference.NewDraft(m.dict.Code())
		draft.Track(source)
		draft.Track(target)
		if _, err := source.MergeInto(m.dict, target, m.now); err != nil {
			return err
		}
		chain, err := m.repo.MergedInto(ctx, m.dict.Code(), source.ID())
		if err != nil {
			return err
		}
		for _, merged := range chain {
			draft.Track(merged).Repoint(target.ID())
		}
		result.Changed, err = m.record(ctx, draft, reference.ChangeMerged, source.ID(), cmd.Meta, 0)
		result.Entry = target.State()
		return err
	})
	result.Revision = revision
	return result, err
}

// AddAlias добавляет вариант; тот же ключ у этой записи — повтор без ревизии.
func (s *Service) AddAlias(ctx context.Context, cmd AddAliasCommand) (EntryResult, error) {
	var result EntryResult
	revision, err := s.mutate(ctx, cmd.Dictionary, func(ctx context.Context, m *session) error {
		entry, err := m.repo.Entry(ctx, m.dict.Code(), cmd.EntryID)
		if err != nil {
			return err
		}
		draft := reference.NewDraft(m.dict.Code())
		draft.Track(entry)
		alias, _, err := entry.AddAlias(m.dict, s.aliasInput(cmd.Alias), cmd.Meta.Source, m.now)
		if err != nil {
			return err
		}
		result.Changed, err = m.record(ctx, draft, reference.ChangeAliasAdded, entry.ID(), cmd.Meta, 0)
		result.Entry, result.Alias = entry.State(), &alias
		return err
	})
	result.Revision = revision
	return result, err
}

// RemoveAlias удаляет вариант; ключ освобождается той же ревизией.
func (s *Service) RemoveAlias(ctx context.Context, cmd RemoveAliasCommand) (EntryResult, error) {
	var result EntryResult
	revision, err := s.mutate(ctx, cmd.Dictionary, func(ctx context.Context, m *session) error {
		entry, err := m.repo.EntryByAlias(ctx, m.dict.Code(), cmd.AliasID)
		if err != nil {
			return err
		}
		draft := reference.NewDraft(m.dict.Code())
		draft.Track(entry)
		alias, err := entry.RemoveAlias(cmd.AliasID, m.now)
		if err != nil {
			return err
		}
		result.Changed, err = m.record(ctx, draft, reference.ChangeAliasRemoved, entry.ID(), cmd.Meta, 0)
		result.Entry, result.Alias = entry.State(), &alias
		return err
	})
	result.Revision = revision
	return result, err
}

// meet считает повторную встречу ключа у его владельца без ревизии журнала:
// счётчик ранжирует очередь кандидатов и не меняет ссылок потребителей.
func (s *Service) meet(ctx context.Context, m *session, owner ids.UUID, result *EntryResult) error {
	entry, err := m.repo.Entry(ctx, m.dict.Code(), owner)
	if err != nil {
		return err
	}
	entry.Meet(m.now)
	if err := m.repo.Save(ctx, []*reference.Entry{entry}, nil); err != nil {
		return err
	}
	result.Entry = entry.State()
	return nil
}

// current — запись для правки: ревизия If-Match обязана совпасть с последней
// ревизией записи, иначе правка легла бы поверх непрочитанного изменения.
func (s *Service) current(ctx context.Context, m *session, id ids.UUID, ifMatch int64) (*reference.Entry, error) {
	entry, err := m.repo.Entry(ctx, m.dict.Code(), id)
	if err != nil {
		return nil, err
	}
	if entry.Revision() != ifMatch {
		return nil, reference.ErrEntryChanged
	}
	return entry, nil
}

// survivor — запись, в которую разрешается id: объединённая ведёт к выжившей за один шаг.
func (s *Service) survivor(ctx context.Context, m *session, id ids.UUID) (*reference.Entry, error) {
	entry, err := m.repo.Entry(ctx, m.dict.Code(), id)
	if err != nil {
		return nil, err
	}
	if entry.Status() == reference.StatusMerged {
		return m.repo.Entry(ctx, m.dict.Code(), entry.MergedInto())
	}
	return entry, nil
}

func (s *Service) aliasInputs(specs []AliasSpec) []reference.AliasInput {
	inputs := make([]reference.AliasInput, 0, len(specs))
	for _, spec := range specs {
		inputs = append(inputs, s.aliasInput(spec))
	}
	return inputs
}

func (s *Service) aliasInput(spec AliasSpec) reference.AliasInput {
	return reference.AliasInput{
		ID:     s.ids.NewUUID(),
		Text:   spec.Text,
		Kind:   spec.Kind,
		Locale: spec.Locale,
		Weight: spec.Weight,
	}
}
