package reference

import "slices"

// DictionarySettings — публикуемые настройки; ревизия и счётчик принадлежат базе.
type DictionarySettings struct {
	Kind       Kind        `json:"kind"`
	Identity   bool        `json:"identity"`
	AliasKinds []AliasKind `json:"aliasKinds"`
	Limits     Limits      `json:"limits"`
}

// Validate проверяет настройки до открытия транзакции импорта.
func (s DictionarySettings) Validate() error {
	if s.Kind != KindOpen && s.Kind != KindClosed {
		return ErrInvalidSettings.WithMessage("kind — open или closed")
	}
	limits := s.Limits
	if limits.MaxEntries < 1 || limits.MaxEntries > DefaultMaxEntries ||
		limits.MaxAliases < 1 || limits.MaxAliases > DefaultMaxAliases ||
		limits.MaxAliasLength < 1 || limits.MaxAliasLength > DefaultMaxAliasLength ||
		limits.MaxAliasWords < 1 || limits.MaxAliasWords > DefaultMaxAliasWords ||
		limits.Normalizer != NormalizerVersion {
		return ErrInvalidSettings.WithMessage("лимиты вне поддерживаемых границ или неизвестный normalizer")
	}
	if !slices.Contains(s.AliasKinds, AliasSpelling) {
		return ErrInvalidSettings.WithMessage("aliasKinds должен разрешать spelling для канонического имени")
	}
	seen := map[AliasKind]bool{}
	for _, kind := range s.AliasKinds {
		if seen[kind] || !slices.Contains(AliasKinds, kind) {
			return ErrInvalidSettings.WithMessage("неизвестный или повторный aliasKinds")
		}
		seen[kind] = true
	}
	return nil
}

// Configure возвращает false при тех же настройках, включая перестановку aliasKinds.
// Сужение правил непустого словаря требует проверки всех записей и не выполняется импортом.
func (d *Dictionary) Configure(settings DictionarySettings) (bool, error) {
	if err := settings.Validate(); err != nil {
		return false, err
	}
	if d.state.Entries > int64(settings.Limits.MaxEntries) {
		return false, ErrSettingsConflict.WithMessage("maxEntries меньше числа существующих записей")
	}
	if d.state.Entries > 0 {
		if err := d.checkSettingsChange(settings); err != nil {
			return false, err
		}
	}
	if d.sameSettings(settings) {
		return false, nil
	}
	d.state.Kind = settings.Kind
	d.state.Identity = settings.Identity
	d.state.AliasKinds = slices.Clone(settings.AliasKinds)
	d.state.Limits = settings.Limits
	return true, nil
}

func (d *Dictionary) sameSettings(settings DictionarySettings) bool {
	if d.state.Kind != settings.Kind || d.state.Identity != settings.Identity ||
		d.state.Limits != settings.Limits || len(d.state.AliasKinds) != len(settings.AliasKinds) {
		return false
	}
	for _, kind := range d.state.AliasKinds {
		if !slices.Contains(settings.AliasKinds, kind) {
			return false
		}
	}
	return true
}

func (d *Dictionary) checkSettingsChange(settings DictionarySettings) error {
	if d.state.Kind != settings.Kind || d.state.Identity != settings.Identity {
		return ErrSettingsConflict.WithMessage("kind и identity нельзя менять у словаря с записями")
	}
	old := d.state.Limits
	next := settings.Limits
	if next.MaxAliases < old.MaxAliases || next.MaxAliasLength < old.MaxAliasLength ||
		next.MaxAliasWords < old.MaxAliasWords {
		return ErrSettingsConflict.WithMessage("нельзя ужесточить лимиты написаний словаря с записями")
	}
	for _, kind := range d.state.AliasKinds {
		if !slices.Contains(settings.AliasKinds, kind) {
			return ErrSettingsConflict.WithMessage("нельзя удалить aliasKinds у словаря с записями")
		}
	}
	return nil
}
