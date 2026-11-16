package referencedata

import (
	"io/fs"

	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
)

// LoadImport проверяет пакет до подключения к базе, используя те же правила, что и операторский импорт.
func LoadImport(source fs.FS, name string) (reference.ImportPackage, error) {
	var input reference.ImportPackage
	if err := decode(source, name, &input); err != nil {
		return input, reference.ErrInvalidDelivery.WithMessage("не удалось прочитать пакет ImportPackage v1 в JSON")
	}
	if _, err := reference.PrepareImport(input); err != nil {
		return input, err
	}
	return input, nil
}
