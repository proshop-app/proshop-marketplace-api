package taxonomy

import (
	"context"
	"errors"
	"sort"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

// RemoveHintCommand доказывает добавление legacy-подсказки прежним неизменяемым снимком.
type RemoveHintCommand struct {
	Attribute       string
	Dictionary      string
	PreviousVersion int64
	ExpectedVersion int64
}

type changeHintDefinition func(context.Context, taxonomy.Repository, *taxonomy.Definition) (bool, error)

// PutDictionaryBinding меняет одно правило по общей версии таксономии.
func (s *Service) PutDictionaryBinding(
	ctx context.Context,
	binding taxonomy.DictionaryBinding,
	expected int64,
) (int64, error) {
	if binding.Dictionaries == nil {
		return 0, taxonomy.ErrInvalid.WithMessage("нужен массив источников подсказок")
	}
	change := func(_ context.Context, _ taxonomy.Repository, next *taxonomy.Definition) (bool, error) {
		replacement := binding
		replacement.Dictionaries = append([]string{}, binding.Dictionaries...)
		index := -1
		for i, current := range next.DictionaryBindings {
			if current.CategoryID == binding.CategoryID && current.AttributeCode == binding.AttributeCode {
				index = i
				break
			}
		}
		rules := append([]taxonomy.DictionaryBinding{}, next.DictionaryBindings...)
		var previous taxonomy.DictionaryBinding
		if index < 0 {
			rules = append(rules, replacement)
		} else {
			previous = rules[index]
			rules[index] = replacement
		}
		next.DictionaryBindings = rules
		if err := next.Validate(); err != nil {
			return false, err
		}
		return index < 0 || !taxonomy.SameDictionaryBinding(previous, replacement), nil
	}
	return s.changeHints(ctx, expected, change)
}

// DeleteDictionaryBinding удаляет правило, возвращая наследование, но не базовый dictionary.
func (s *Service) DeleteDictionaryBinding(
	ctx context.Context,
	categoryID, attributeCode string,
	expected int64,
) (int64, error) {
	change := func(_ context.Context, _ taxonomy.Repository, next *taxonomy.Definition) (bool, error) {
		// Даже отсутствующее правило должно адресовать настоящий string в этой области.
		probe := *next
		probe.DictionaryBindings = []taxonomy.DictionaryBinding{
			{
				CategoryID:    categoryID,
				AttributeCode: attributeCode,
				Mode:          taxonomy.BindingReplace,
				Dictionaries:  []string{},
			},
		}
		if err := probe.Validate(); err != nil {
			return false, err
		}
		rules := make([]taxonomy.DictionaryBinding, 0, len(next.DictionaryBindings))
		changed := false
		for _, rule := range next.DictionaryBindings {
			if rule.CategoryID == categoryID && rule.AttributeCode == attributeCode {
				changed = true
				continue
			}
			rules = append(rules, rule)
		}
		next.DictionaryBindings = rules
		return changed, nil
	}
	return s.changeHints(ctx, expected, change)
}

// RemoveHint убирает только доказанную добавленную строковую подсказку, не откатывая другие данные.
func (s *Service) RemoveHint(ctx context.Context, command RemoveHintCommand) (int64, error) {
	if !taxonomy.ValidCode(command.Attribute) || !taxonomy.ValidCode(command.Dictionary) ||
		command.PreviousVersion < 1 || command.PreviousVersion >= command.ExpectedVersion {
		return 0, taxonomy.ErrInvalid.WithMessage("нужны коды и предшествующая версия без привязки")
	}
	return s.changeHints(
		ctx,
		command.ExpectedVersion,
		func(ctx context.Context, repository taxonomy.Repository, next *taxonomy.Definition) (bool, error) {
			previous, err := repository.Version(ctx, command.PreviousVersion)
			if err != nil {
				return false, err
			}
			old, found := previous.Fields()[command.Attribute]
			current, exists := next.Fields()[command.Attribute]
			if !found || !exists || old.Type != "string" || old.Dictionary != "" ||
				current.Type != old.Type || current.Unit != old.Unit || current.Dictionary != command.Dictionary {
				return false, taxonomy.ErrConflict.WithMessage("история не доказывает добавленную подсказку")
			}
			original := *next
			next.Attributes = append([]taxonomy.Attribute(nil), next.Attributes...)
			for i := range next.Attributes {
				if next.Attributes[i].Code == command.Attribute {
					next.Attributes[i].Dictionary = ""
				}
			}
			if err := next.CheckRollbackEvolution(original); err != nil {
				return false, err
			}
			return true, nil
		})
}

func (s *Service) changeHints(ctx context.Context, expected int64, change changeHintDefinition) (int64, error) {
	var version int64
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		repository := tx.Taxonomy()
		if err := repository.Lock(ctx); err != nil {
			return err
		}
		next, err := repository.Current(ctx)
		if err != nil {
			return err
		}
		if expected != next.Version || next.Version >= taxonomy.MaxTaxonomyVersion {
			return taxonomy.ErrConflict
		}
		version = next.Version
		changed, err := change(ctx, repository, &next)
		if err != nil || !changed {
			return err
		}
		if err := next.Validate(); err != nil {
			return err
		}
		if err := validateOpenHintSources(ctx, tx.Reference(), next.DictionaryBindings); err != nil {
			return err
		}
		// Подсказки не меняют валидацию карточек: закрытые Values и товарные проекции остаются как есть.
		next.Version++
		if err := repository.Save(ctx, next, version); err != nil {
			return err
		}
		version = next.Version
		return nil
	})
	return version, err
}

func validateOpenHintSources(
	ctx context.Context,
	registry ports.TaxonomyDictionaries,
	rules []taxonomy.DictionaryBinding,
) error {
	unique := map[string]bool{}
	for _, rule := range rules {
		for _, code := range rule.Dictionaries {
			unique[code] = true
		}
	}
	codes := make([]string, 0, len(unique))
	for code := range unique {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	for _, code := range codes {
		dictionary, err := registry.LockDictionary(ctx, code)
		if err != nil {
			var typed *errs.Error
			if errors.As(err, &typed) && typed.Code() == errs.NotFound {
				return taxonomy.ErrInvalid.WithMessage("словарь не существует: " + code)
			}
			return err
		}
		if dictionary == nil || dictionary.State().Kind != reference.KindOpen {
			return taxonomy.ErrInvalid.WithMessage("источник подсказок должен быть open: " + code)
		}
	}
	return nil
}
