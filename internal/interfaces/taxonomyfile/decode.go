// Package taxonomyfile разбирает один строгий YAML-документ оператора.
package taxonomyfile

import (
	"errors"
	"io"

	"gopkg.in/yaml.v3"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

// Decode запрещает неизвестные поля и дополнительные документы.
func Decode(reader io.Reader) (taxonomy.Definition, error) {
	var d taxonomy.Definition
	decoder := yaml.NewDecoder(reader)
	decoder.KnownFields(true)
	if err := decoder.Decode(&d); err != nil {
		return d, taxonomy.ErrInvalid.Wrap(err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return d, taxonomy.ErrInvalid.WithMessage("ожидался один YAML-документ")
	}
	return d, d.Validate()
}
