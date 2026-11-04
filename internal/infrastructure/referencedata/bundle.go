// Package referencedata читает поставку реестра из каталога: manifest.json с
// номером, суммой и счётчиками и entries.json с записями. Данные проверяются
// до обращения к API; реальное применение выполняет только владелец platform.
package referencedata

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"unicode/utf8"
)

const (
	manifestFile = "manifest.json"
	entriesFile  = "entries.json"
)

// ErrInvalidBundle — файлы поставки расходятся с манифестом или не разбираются.
var (
	ErrInvalidBundle = errors.New("некорректный комплект поставки реестра")
	dictionaryCode   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
)

// Summary — проверяемый состав поставки: сверяется с манифестом и печатается командой check.
type Summary struct {
	Dictionary string `json:"dictionary"`
	Release    int64  `json:"release"`
	Checksum   string `json:"checksum"`
	Entries    int    `json:"entries"`
	Confirmed  int    `json:"confirmed"`
	Candidates int    `json:"candidates"`
	Aliases    int    `json:"aliases"`
}

// Delivery — передаваемый владельцу комплект, без локального применения.
type Delivery struct {
	Dictionary  string          `json:"-"`
	Version     int64           `json:"release"`
	Checksum    string          `json:"checksum"`
	Entries     []DeliveryEntry `json:"-"`
	EntriesJSON string          `json:"entriesJson"`
}

// DeliveryEntry — JSON записи поставки для проверки файла и передачи владельцу.
type DeliveryEntry struct {
	Canonical string          `json:"canonical"`
	Status    string          `json:"status"`
	Aliases   []DeliveryAlias `json:"aliases,omitempty"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
}

// DeliveryAlias — исходный вариант написания, семантику проверяет владелец.
type DeliveryAlias struct {
	Text   string `json:"text"`
	Kind   string `json:"kind,omitempty"`
	Locale string `json:"locale,omitempty"`
}

type manifest struct {
	Dictionary string `json:"dictionary"`
	Release    int64  `json:"release"`
	Checksum   string `json:"checksum"`
	Entries    int    `json:"entries"`
	Aliases    int    `json:"aliases"`
}

// Load читает и проверяет поставку. Сумма — SHA-256 файла entries.json:
// изменённые данные с прежним номером дадут конфликт при apply.
func Load(source fs.FS) (Delivery, Summary, error) {
	var header manifest
	if err := decode(source, manifestFile, &header); err != nil {
		return Delivery{}, Summary{}, err
	}
	body, err := fs.ReadFile(source, entriesFile)
	if err != nil || !utf8.Valid(body) {
		return Delivery{}, Summary{}, fmt.Errorf("%w: %s", ErrInvalidBundle, entriesFile)
	}
	sum := sha256.Sum256(body)
	checksum := hex.EncodeToString(sum[:])
	if checksum != header.Checksum {
		return Delivery{}, Summary{}, fmt.Errorf("%w: сумма %s — %s", ErrInvalidBundle, entriesFile, checksum)
	}
	var entries []DeliveryEntry
	if err := strict(body, &entries); err != nil {
		return Delivery{}, Summary{}, fmt.Errorf("%w: %s: %w", ErrInvalidBundle, entriesFile, err)
	}
	summary := Summary{
		Dictionary: header.Dictionary,
		Release:    header.Release,
		Checksum:   checksum,
		Entries:    len(entries),
	}
	for _, entry := range entries {
		summary.Aliases += len(entry.Aliases)
		if entry.Status == "confirmed" {
			summary.Confirmed++
		} else {
			summary.Candidates++
		}
	}
	if summary.Entries != header.Entries || summary.Aliases != header.Aliases {
		err := fmt.Errorf("%w: счётчики манифеста расходятся с данными", ErrInvalidBundle)
		return Delivery{}, Summary{}, err
	}
	delivery := Delivery{
		Dictionary:  header.Dictionary,
		Version:     header.Release,
		Checksum:    checksum,
		Entries:     entries,
		EntriesJSON: string(body),
	}
	if !dictionaryCode.MatchString(delivery.Dictionary) || delivery.Version < 1 ||
		delivery.Version > 9007199254740991 || len(entries) == 0 {
		return Delivery{}, Summary{}, ErrInvalidBundle
	}
	for _, entry := range entries {
		if entry.Status != "candidate" && entry.Status != "confirmed" || entry.Canonical == "" {
			return Delivery{}, Summary{}, ErrInvalidBundle
		}
	}
	return delivery, summary, nil
}

func decode(source fs.FS, name string, target any) error {
	body, err := fs.ReadFile(source, name)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidBundle, name)
	}
	if err := strict(body, target); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrInvalidBundle, name, err)
	}
	return nil
}

// strict — неизвестные поля и данные после документа — ошибка: опечатка в
// поле поставки не должна молча пропасть.
func strict(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("лишние данные после документа")
	}
	return nil
}
