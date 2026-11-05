package reference

import (
	"regexp"
	"slices"
	"strconv"
	"unicode"
)

// Kind — открытый словарь растёт от продавцов и оператора, закрытый — перечисление схемы.
type Kind string

const (
	// KindOpen — открытый словарь: записи заводят оператор, поставки и кандидаты каталога.
	KindOpen Kind = "open"
	// KindClosed — закрытый словарь: перечисление, которое меняется только поставкой.
	KindClosed Kind = "closed"
)

// Значения лимитов по умолчанию из плана реестра.
const (
	DefaultMaxEntries     = 1_000_000
	DefaultMaxAliases     = 50
	DefaultMaxAliasLength = 120
	DefaultMaxAliasWords  = 8
)

var codePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// Limits — лимиты словаря; Normalizer — версия правила, которым построены ключи.
type Limits struct {
	MaxEntries     int    `json:"maxEntries"`
	MaxAliases     int    `json:"maxAliases"`
	MaxAliasLength int    `json:"maxAliasLength"`
	MaxAliasWords  int    `json:"maxAliasWords"`
	Normalizer     string `json:"normalizer"`
}

// DictionaryState — настройки словаря и его голова журнала.
type DictionaryState struct {
	Code       string
	Kind       Kind
	Identity   bool
	AliasKinds []AliasKind
	Limits     Limits
	// Revision — последняя ревизия журнала словаря; растёт под блокировкой строки словаря,
	// поэтому ревизии одного словаря видны потребителю в порядке фиксации.
	Revision int64
	// Entries — записей в словаре, считается в транзакции изменения без count(*).
	Entries int64
}

// Dictionary — словарь реестра. Код ничего не знает о конкретном словаре,
// кроме этих настроек.
type Dictionary struct {
	state DictionaryState
}

// ValidCode — код словаря из адреса: строчная латиница, цифры, подчёркивание.
func ValidCode(code string) bool {
	return codePattern.MatchString(code)
}

// RestoreDictionary поднимает словарь из хранилища.
func RestoreDictionary(state DictionaryState) *Dictionary {
	state.AliasKinds = slices.Clone(state.AliasKinds)
	return &Dictionary{state: state}
}

// State — снимок словаря для хранения.
func (d *Dictionary) State() DictionaryState {
	state := d.state
	state.AliasKinds = slices.Clone(d.state.AliasKinds)
	return state
}

// Code — код словаря.
func (d *Dictionary) Code() string { return d.state.Code }

// Revision — последняя ревизия журнала словаря.
func (d *Dictionary) Revision() int64 { return d.state.Revision }

// Limits — лимиты словаря.
func (d *Dictionary) Limits() Limits { return d.state.Limits }

// Normalizer — версия правила ключа, которым построен словарь.
func (d *Dictionary) Normalizer() string { return d.state.Limits.Normalizer }

// CheckNormalizer не даёт писать ключи правилом, которым словарь не построен:
// иначе одно написание получило бы два ключа.
func (d *Dictionary) CheckNormalizer() error {
	if d.state.Limits.Normalizer != NormalizerVersion {
		return ErrNormalizer
	}
	return nil
}

// AllowsKind — вид варианта разрешён настройками словаря.
func (d *Dictionary) AllowsKind(kind AliasKind) bool {
	return slices.Contains(d.state.AliasKinds, kind)
}

// Advance переносит голову журнала на новую ревизию и сдвигает счётчик записей.
// Лимит записей проверяется здесь: один счётчик на все пути создания.
func (d *Dictionary) Advance(revision, entries int64) error {
	if revision <= d.state.Revision {
		return ErrInvalid.WithMessage("ревизия журнала не растёт")
	}
	total := d.state.Entries + entries
	if entries > 0 && total > int64(d.state.Limits.MaxEntries) {
		return ErrLimit.WithMessage("в словаре не больше " + strconv.Itoa(d.state.Limits.MaxEntries) + " записей")
	}
	d.state.Revision = revision
	d.state.Entries = max(total, 0)
	return nil
}

// Spelling приводит написание к хранимому виду и проверяет лимиты словаря:
// длина в символах и число слов считаются после схлопывания пробелов.
func (d *Dictionary) Spelling(text string) (clean, key string, err error) {
	clean = Collapse(text)
	if clean == "" {
		return "", "", ErrInvalid.WithMessage("написание пустое")
	}
	for _, r := range clean {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return "", "", ErrInvalid.WithMessage("написание содержит управляющие символы")
		}
	}
	limits := d.state.Limits
	if Length(clean) > limits.MaxAliasLength {
		return "", "", ErrLimit.WithMessage("написание длиннее " + strconv.Itoa(limits.MaxAliasLength) + " символов")
	}
	if len(Words(clean)) > limits.MaxAliasWords {
		return "", "", ErrLimit.WithMessage("в написании больше " + strconv.Itoa(limits.MaxAliasWords) + " слов")
	}
	key = Key(clean)
	if key == "" {
		return "", "", ErrInvalid.WithMessage("у написания пустой ключ")
	}
	return clean, key, nil
}
