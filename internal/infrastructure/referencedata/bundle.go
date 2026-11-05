// Package referencedata читает поставку реестра из каталога: manifest.json с
// номером, суммой и счётчиками и entries.json с записями. Данные проверяются
// до подключения к базе; реальные списки владелец кладёт отдельным каталогом.
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

	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
)

const (
	manifestFile = "manifest.json"
	entriesFile  = "entries.json"
)

// ErrInvalidBundle — файлы поставки расходятся с манифестом или не разбираются.
var ErrInvalidBundle = errors.New("некорректный комплект поставки реестра")

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

type manifest struct {
	Dictionary string `json:"dictionary"`
	Release    int64  `json:"release"`
	Checksum   string `json:"checksum"`
	Entries    int    `json:"entries"`
	Aliases    int    `json:"aliases"`
}

// Load читает и проверяет поставку. Сумма — SHA-256 файла entries.json:
// изменённые данные с прежним номером дадут конфликт при apply.
func Load(source fs.FS) (reference.Delivery, Summary, error) {
	var header manifest
	if err := decode(source, manifestFile, &header); err != nil {
		return reference.Delivery{}, Summary{}, err
	}
	body, err := fs.ReadFile(source, entriesFile)
	if err != nil {
		return reference.Delivery{}, Summary{}, fmt.Errorf("%w: %s", ErrInvalidBundle, entriesFile)
	}
	sum := sha256.Sum256(body)
	checksum := hex.EncodeToString(sum[:])
	if checksum != header.Checksum {
		return reference.Delivery{}, Summary{}, fmt.Errorf("%w: сумма %s — %s", ErrInvalidBundle, entriesFile, checksum)
	}
	var entries []reference.DeliveryEntry
	if err := strict(body, &entries); err != nil {
		return reference.Delivery{}, Summary{}, fmt.Errorf("%w: %s: %w", ErrInvalidBundle, entriesFile, err)
	}
	summary := Summary{
		Dictionary: header.Dictionary,
		Release:    header.Release,
		Checksum:   checksum,
		Entries:    len(entries),
	}
	for _, entry := range entries {
		summary.Aliases += len(entry.Aliases)
		if entry.Status == reference.StatusConfirmed {
			summary.Confirmed++
		} else {
			summary.Candidates++
		}
	}
	if summary.Entries != header.Entries || summary.Aliases != header.Aliases {
		err := fmt.Errorf("%w: счётчики манифеста расходятся с данными", ErrInvalidBundle)
		return reference.Delivery{}, Summary{}, err
	}
	delivery := reference.Delivery{
		Dictionary: header.Dictionary,
		Version:    header.Release,
		Checksum:   checksum,
		Entries:    entries,
	}
	if err := reference.ValidateDelivery(delivery); err != nil {
		return reference.Delivery{}, Summary{}, err
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
