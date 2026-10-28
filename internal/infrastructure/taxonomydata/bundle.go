// Package taxonomydata компилирует собственные данные uno в полный контракт таксономии.
package taxonomydata

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

const dataDirectory = "data/"

var (
	//go:embed data/*.json
	bundled   embed.FS
	dataFiles = []string{"attributes.json", "schemas.json", "categories.json"}
	// ErrInvalidBundle — данные расходятся с манифестом или нарушают ссылки схем.
	ErrInvalidBundle = errors.New("некорректный комплект данных каталога")
)

// Coverage — проверяемый охват поставки, а не обещание перечислить все товары мира.
type Coverage struct {
	Directions int `json:"directions"`
	Categories int `json:"categories"`
	Leaves     int `json:"leaves"`
	Schemas    int `json:"schemas"`
	Attributes int `json:"attributes"`
	Values     int `json:"values"`
}

type manifest struct {
	Release  int64    `json:"release"`
	Checksum string   `json:"checksum"`
	Coverage Coverage `json:"coverage"`
}

type attributeSchema struct {
	Attributes []string `json:"attributes"`
	Required   []string `json:"required"`
}

type categorySource struct {
	ID       string           `json:"id"`
	Name     string           `json:"name"`
	Schema   string           `json:"schema,omitempty"`
	Children []categorySource `json:"children,omitempty"`
}

type compiler struct {
	definitions map[string]taxonomy.Attribute
	schemas     map[string]attributeSchema
	usedSchemas map[string]bool
	usedFields  map[string]bool
	coverage    Coverage
}

// Load проверяет встроенный комплект до подключения к БД.
func Load() (taxonomy.Release, Coverage, error) {
	return load(bundled)
}

func load(source fs.FS) (taxonomy.Release, Coverage, error) {
	var header manifest
	var definitions []taxonomy.Attribute
	var categories []categorySource
	state := compiler{
		definitions: make(map[string]taxonomy.Attribute),
		usedSchemas: make(map[string]bool),
		usedFields:  make(map[string]bool),
	}
	for file, target := range map[string]any{
		"manifest.json":   &header,
		"attributes.json": &definitions,
		"schemas.json":    &state.schemas,
		"categories.json": &categories,
	} {
		if err := decodeFile(source, file, target); err != nil {
			return taxonomy.Release{}, Coverage{}, err
		}
	}
	checksum, err := bundleChecksum(source)
	if err != nil || checksum != header.Checksum {
		return taxonomy.Release{}, Coverage{}, ErrInvalidBundle
	}
	for _, definition := range definitions {
		if _, exists := state.definitions[definition.Code]; exists || definition.Required {
			return taxonomy.Release{}, Coverage{}, ErrInvalidBundle
		}
		state.definitions[definition.Code] = definition
		state.coverage.Values += len(definition.Values)
	}
	state.coverage.Directions = len(categories)
	state.coverage.Attributes = len(definitions)
	state.coverage.Schemas = len(state.schemas)
	tree, err := state.compile(categories)
	if err != nil || state.coverage != header.Coverage || len(state.usedSchemas) != len(state.schemas) ||
		len(state.usedFields) != len(definitions) {
		return taxonomy.Release{}, Coverage{}, ErrInvalidBundle
	}
	release := taxonomy.Release{
		Version:    header.Release,
		Checksum:   header.Checksum,
		Categories: tree,
	}
	if err := taxonomy.ValidateRelease(release); err != nil {
		return taxonomy.Release{}, Coverage{}, err
	}

	return release, state.coverage, nil
}

func (c *compiler) compile(sources []categorySource) ([]taxonomy.Category, error) {
	categories := make([]taxonomy.Category, 0, len(sources))
	names := make(map[string]bool)
	for _, source := range sources {
		if names[source.Name] {
			return nil, ErrInvalidBundle
		}
		names[source.Name] = true
		c.coverage.Categories++
		category := taxonomy.Category{
			ID:         source.ID,
			Name:       source.Name,
			Attributes: []taxonomy.Attribute{},
			Children:   []taxonomy.Category{},
		}
		var err error
		if len(source.Children) > 0 {
			if source.Schema != "" {
				return nil, ErrInvalidBundle
			}
			category.Children, err = c.compile(source.Children)
		} else {
			c.coverage.Leaves++
			category.Attributes, err = c.fields(source.Schema)
		}
		if err != nil {
			return nil, err
		}
		categories = append(categories, category)
	}

	return categories, nil
}

func (c *compiler) fields(key string) ([]taxonomy.Attribute, error) {
	schema, exists := c.schemas[key]
	if !exists || len(schema.Attributes) == 0 {
		return nil, ErrInvalidBundle
	}
	c.usedSchemas[key] = true
	required := make(map[string]bool)
	for _, code := range schema.Required {
		if required[code] {
			return nil, ErrInvalidBundle
		}
		required[code] = true
	}
	fields := make([]taxonomy.Attribute, 0, len(schema.Attributes))
	seen := make(map[string]bool)
	for _, code := range schema.Attributes {
		field, exists := c.definitions[code]
		if !exists || seen[code] {
			return nil, ErrInvalidBundle
		}
		seen[code] = true
		c.usedFields[code] = true
		field.Required = required[code]
		fields = append(fields, field)
	}
	for code := range required {
		if !seen[code] {
			return nil, ErrInvalidBundle
		}
	}

	return fields, nil
}

func decodeFile(source fs.FS, name string, target any) error {
	body, err := fs.ReadFile(source, dataDirectory+name)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidBundle, name)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrInvalidBundle, name, err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: лишние данные в %s", ErrInvalidBundle, name)
	}

	return nil
}

func bundleChecksum(source fs.FS) (string, error) {
	hash := sha256.New()
	for _, name := range dataFiles {
		body, err := fs.ReadFile(source, dataDirectory+name)
		if err != nil {
			return "", err
		}
		_, _ = io.Copy(hash, strings.NewReader(name+"\x00"))
		_, _ = hash.Write(body)
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}
