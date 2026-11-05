// Package taxonomyfile разбирает части набора без вычисляемых значений реестра.
package taxonomyfile

import (
	"bytes"
	"errors"
	"io"

	"gopkg.in/yaml.v3"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

// DecodePart допускает только поля выбранной части; ссылки проверяются после соединения.
func DecodePart(reader io.Reader, part string) (taxonomy.Definition, error) {
	var d taxonomy.Definition
	target, err := partTarget(&d, part)
	if err != nil {
		return d, err
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return d, taxonomy.ErrInvalid.Wrap(err)
	}
	var node yaml.Node
	if err := yaml.Unmarshal(body, &node); err != nil {
		return d, taxonomy.ErrInvalid.Wrap(err)
	}
	if hasReferences(&node) {
		return d, taxonomy.ErrInvalid.WithMessage("якоря и ссылки YAML запрещены")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(body))
	decoder.KnownFields(true)
	if err := decoder.Decode(target); err != nil {
		return d, taxonomy.ErrInvalid.Wrap(err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return d, taxonomy.ErrInvalid.WithMessage("ожидался один YAML-документ")
	}
	return d, nil
}

// EncodePart не выдаёт вычисляемые Values, поэтому файл пригоден для обратной загрузки.
func EncodePart(d taxonomy.Definition, part string) ([]byte, error) {
	target, err := partTarget(&d, part)
	if err != nil {
		return nil, err
	}
	return yaml.Marshal(target)
}

func partTarget(d *taxonomy.Definition, part string) (any, error) {
	switch part {
	case "attributes":
		return &d.Attributes, nil
	case "schemas":
		return &d.Schemas, nil
	case "categories":
		return &d.Categories, nil
	default:
		return nil, taxonomy.ErrInvalid.WithMessage("неизвестная часть таксономии")
	}
}

func hasReferences(node *yaml.Node) bool {
	if node.Anchor != "" || node.Kind == yaml.AliasNode {
		return true
	}
	for _, child := range node.Content {
		if hasReferences(child) {
			return true
		}
	}
	return false
}
