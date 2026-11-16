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

// DeliveryRecord — применённый выпуск и диапазон ревизий записей.
type DeliveryRecord struct {
	Dictionary   string
	Version      int64
	Checksum     string
	RevisionFrom int64
	RevisionTo   int64
	AppliedAt    time.Time
}

// DeliveryResult — внутренний итог применения выпуска.
type DeliveryResult struct {
	Changed bool
	Created int
	Updated int
	Skipped int
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

// validateDelivery проверяет записи по настройкам заголовка пакета.
func validateDelivery(d Delivery, dict *Dictionary, allowEmpty bool) error {
	checksum, err := hex.DecodeString(d.Checksum)
	if !ValidCode(d.Dictionary) || d.Version < 1 || d.Version > MaxDeliveryVersion || err != nil ||
		len(checksum) != checksumSize || d.Checksum != strings.ToLower(d.Checksum) ||
		(!allowEmpty && len(d.Entries) == 0) {
		return ErrInvalidDelivery
	}
	owners := map[string]int{}
	for i, entry := range d.Entries {
		place := "запись " + strconv.Itoa(i+1)
		if entry.Status != StatusCandidate && entry.Status != StatusConfirmed {
			return ErrInvalidDelivery.WithMessage(place + ": статус candidate или confirmed")
		}
		if _, err := CleanMetadata(entry.Metadata); err != nil {
			return ErrInvalidDelivery.WithMessage(place + ": " + messageOf(err))
		}
		if _, _, err := dict.CanonicalSpelling(entry.Canonical); err != nil {
			return ErrInvalidDelivery.WithMessage(place + ": " + messageOf(err))
		}
		if err := ValidateColorState(dict, EntryState{
			Dictionary:    d.Dictionary,
			CanonicalName: entry.Canonical,
			Status:        entry.Status,
			Metadata:      entry.Metadata,
		}); err != nil {
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

// PlanDelivery пропускает повтор того же выпуска с той же суммой.
// Устаревший выпуск или та же версия с другой суммой — конфликт.
func PlanDelivery(latest *DeliveryRecord, d Delivery) (bool, error) {
	if latest == nil || d.Version > latest.Version {
		return true, nil
	}
	if d.Version < latest.Version || d.Checksum != latest.Checksum {
		return false, ErrDeliveryConflict
	}
	return false, nil
}

func messageOf(err error) string {
	if typed, ok := errs.As(err); ok {
		return typed.Message()
	}
	return err.Error()
}
