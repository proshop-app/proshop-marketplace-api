// Package referencedata читает и проверяет JSON-пакет импорта до подключения к базе.
package referencedata

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"

	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
)

// LoadImport проверяет пакет до подключения к базе, используя те же правила, что и операторский импорт.
func LoadImport(source fs.FS, name string) (reference.ImportPackage, error) {
	var input reference.ImportPackage
	body, err := fs.ReadFile(source, name)
	if err != nil {
		return input, reference.ErrInvalidDelivery.WithMessage("не удалось прочитать файл пакета")
	}
	if err := decodeStrict(body, &input); err != nil {
		if errors.Is(err, reference.ErrInvalidSettings) {
			return input, err
		}
		return input, reference.ErrInvalidDelivery.WithMessage("не удалось прочитать пакет ImportPackage v2 в JSON")
	}
	if _, err := reference.PrepareImport(input); err != nil {
		return input, err
	}
	return input, nil
}

func decodeStrict(body []byte, target any) error {
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
