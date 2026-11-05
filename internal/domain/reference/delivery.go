package reference

import (
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

const (
	checksumSize = 32
	// MaxDeliveryVersion сохраняет точность номера у JSON-клиентов JavaScript.
	MaxDeliveryVersion int64 = 9007199254740991
)

// AliasKinds — все виды вариантов первого этапа; ими поставка проверяется до словаря.
var AliasKinds = []AliasKind{
	AliasSpelling,
	AliasTranslit,
	AliasAbbreviation,
	AliasFormer,
	AliasLocale,
}

// DeliveryAlias — вариант записи в файле поставки.
type DeliveryAlias struct {
	Text   string    `json:"text"`
	Kind   AliasKind `json:"kind,omitempty"`
	Locale string    `json:"locale,omitempty"`
}

// DeliveryEntry — запись в файле поставки: подтверждённый список или кандидаты каталога.
type DeliveryEntry struct {
	Canonical string          `json:"canonical"`
	Status    Status          `json:"status"`
	Aliases   []DeliveryAlias `json:"aliases,omitempty"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
}

// Delivery — неизменяемая поставка словаря; номер независим от ревизий журнала.
type Delivery struct {
	Dictionary string
	Version    int64
	Checksum   string
	Entries    []DeliveryEntry
}

// DeliveryRecord — применённая поставка и её диапазон ревизий для отката.
type DeliveryRecord struct {
	Dictionary   string
	Version      int64
	Checksum     string
	RevisionFrom int64
	RevisionTo   int64
	AppliedAt    time.Time
	RolledBack   bool
	// RollbackRevision — голова словаря после отката.
	RollbackRevision int64
}

// DeliveryResult — итог apply или rollback; повтор сообщает Changed=false.
type DeliveryResult struct {
	Dictionary string `json:"dictionary"`
	Release    int64  `json:"release"`
	Revision   int64  `json:"revision"`
	Changed    bool   `json:"changed"`
	RolledBack bool   `json:"rolledBack"`
	Created    int    `json:"created"`
	Updated    int    `json:"updated"`
	Skipped    int    `json:"skipped"`
}

// DefaultLimits — лимиты словаря из плана реестра с текущей версией нормализации.
func DefaultLimits() Limits {
	return Limits{
		MaxEntries:     DefaultMaxEntries,
		MaxAliases:     DefaultMaxAliases,
		MaxAliasLength: DefaultMaxAliasLength,
		MaxAliasWords:  DefaultMaxAliasWords,
		Normalizer:     NormalizerVersion,
	}
}

// ValidateDelivery проверяет поставку без хранилища: номер, сумму, статусы,
// лимиты по умолчанию и то, что ни один ключ не встречается у двух записей.
func ValidateDelivery(d Delivery) error {
	checksum, err := hex.DecodeString(d.Checksum)
	if !ValidCode(d.Dictionary) || d.Version < 1 || d.Version > MaxDeliveryVersion || err != nil ||
		len(checksum) != checksumSize || d.Checksum != strings.ToLower(d.Checksum) || len(d.Entries) == 0 {
		return ErrInvalidDelivery
	}
	dict := RestoreDictionary(DictionaryState{Code: d.Dictionary, AliasKinds: AliasKinds, Limits: DefaultLimits()})
	owners := map[string]int{}
	for i, entry := range d.Entries {
		place := "запись " + strconv.Itoa(i+1)
		if entry.Status != StatusCandidate && entry.Status != StatusConfirmed {
			return ErrInvalidDelivery.WithMessage(place + ": статус candidate или confirmed")
		}
		if _, err := CleanMetadata(entry.Metadata); err != nil {
			return ErrInvalidDelivery.WithMessage(place + ": " + messageOf(err))
		}
		texts := []string{entry.Canonical}
		if len(entry.Aliases)+1 > dict.Limits().MaxAliases {
			return ErrInvalidDelivery.WithMessage(place + ": слишком много вариантов")
		}
		for _, alias := range entry.Aliases {
			if alias.Kind != "" && !dict.AllowsKind(alias.Kind) {
				return ErrInvalidDelivery.WithMessage(place + ": неизвестный вид варианта " + string(alias.Kind))
			}
			texts = append(texts, alias.Text)
		}
		for _, text := range texts {
			_, key, err := dict.Spelling(text)
			if err != nil {
				return ErrInvalidDelivery.WithMessage(place + ": " + messageOf(err))
			}
			if owner, taken := owners[key]; taken && owner != i {
				message := place + ": ключ «" + key + "» уже есть у записи " + strconv.Itoa(owner+1)
				return ErrInvalidDelivery.WithMessage(message)
			}
			owners[key] = i
		}
	}
	return nil
}

// PlanDelivery решает, применять ли поставку: повтор той же ничего не меняет, понижение номера,
// подмена данных и повтор откаченной поставки — конфликт.
func PlanDelivery(latest *DeliveryRecord, d Delivery) (bool, error) {
	if latest == nil || d.Version > latest.Version {
		return true, nil
	}
	if d.Version < latest.Version || d.Checksum != latest.Checksum || latest.RolledBack {
		return false, ErrDeliveryConflict
	}
	return false, nil
}

// PlanDeliveryRollback откатывает только последнюю поставку и только пока
// после неё в словаре не было правок: иначе откат затёр бы оператора.
func PlanDeliveryRollback(latest *DeliveryRecord, dict *Dictionary, d Delivery) (bool, error) {
	if latest == nil || latest.Version != d.Version || latest.Checksum != d.Checksum {
		return false, ErrDeliveryConflict
	}
	if latest.RolledBack {
		return false, nil
	}
	if dict.Revision() != latest.RevisionTo {
		return false, ErrDeliveryConflict.WithMessage("после поставки в словаре были правки")
	}
	return true, nil
}

func messageOf(err error) string {
	if typed, ok := errs.As(err); ok {
		return typed.Message()
	}
	return err.Error()
}
