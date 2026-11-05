// Package taxonomydata проверяет нормализованный комплект до подключения к БД.
package taxonomydata

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"

	"gopkg.in/yaml.v3"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

const dataDirectory = "data/"

var (
	//go:embed data/*.yaml
	bundled   embed.FS
	dataFiles = []string{"attributes.yaml", "schemas.yaml", "categories.yaml"}
	// ErrInvalidBundle отделяет ошибки файлов поставки от ошибок структуры домена.
	ErrInvalidBundle = errors.New("некорректный комплект данных каталога")
)

// Coverage проверяет полноту привязок поставки.
type Coverage struct {
	Directions   int `json:"directions" yaml:"directions"`
	Categories   int `json:"categories" yaml:"categories"`
	Leaves       int `json:"leaves" yaml:"leaves"`
	Schemas      int `json:"schemas" yaml:"schemas"`
	Attributes   int `json:"attributes" yaml:"attributes"`
	Dictionaries int `json:"dictionaries" yaml:"dictionaries"`
}

// Manifest связывает номер поставки с точными байтами трёх файлов.
type Manifest struct {
	Release  int64    `json:"release" yaml:"release"`
	Checksum string   `json:"checksum" yaml:"checksum"`
	Coverage Coverage `json:"coverage" yaml:"coverage"`
}

// Load проверяет встроенный комплект до подключения к БД.
func Load() (taxonomy.Release, Coverage, error) { return load(bundled) }

func load(source fs.FS) (taxonomy.Release, Coverage, error) {
	var header Manifest
	if err := decodeFile(source, "manifest.yaml", &header); err != nil {
		return taxonomy.Release{}, Coverage{}, err
	}
	release, coverage, err := describe(source)
	if err != nil {
		return release, coverage, err
	}
	release.Version = header.Release
	if release.Checksum != header.Checksum || coverage != header.Coverage {
		return taxonomy.Release{}, Coverage{}, ErrInvalidBundle
	}
	return release, coverage, taxonomy.ValidateRelease(release)
}

func describe(source fs.FS) (taxonomy.Release, Coverage, error) {
	release := taxonomy.Release{Version: 1}
	var coverage Coverage
	for file, target := range map[string]any{
		"attributes.yaml": &release.Attributes,
		"schemas.yaml":    &release.Schemas,
		"categories.yaml": &release.Categories,
	} {
		if err := decodeFile(source, file, target); err != nil {
			return release, coverage, err
		}
	}
	checksum, err := bundleChecksum(source)
	if err != nil {
		return release, coverage, err
	}
	release.Checksum = checksum
	if err := taxonomy.ValidateRelease(release); err != nil {
		return release, coverage, err
	}
	coverage.Directions = len(release.Categories)
	coverage.Attributes = len(release.Attributes)
	coverage.Schemas = len(release.Schemas)
	usedSchemas := map[string]bool{}
	usedFields := map[string]bool{}
	var walk func([]taxonomy.Category)
	walk = func(categories []taxonomy.Category) {
		for _, category := range categories {
			coverage.Categories++
			if len(category.Children) == 0 {
				coverage.Leaves++
				usedSchemas[category.Schema] = true
			}
			walk(category.Children)
		}
	}
	walk(release.Categories)
	for _, schema := range release.Schemas {
		for _, code := range schema.Attributes {
			usedFields[code] = true
		}
	}
	for _, a := range release.Attributes {
		if a.Dictionary != "" {
			coverage.Dictionaries++
		}
	}
	if len(usedSchemas) != coverage.Schemas || len(usedFields) != coverage.Attributes {
		return release, coverage, ErrInvalidBundle
	}
	return release, coverage, nil
}

func decodeFile(source fs.FS, name string, target any) error {
	body, err := fs.ReadFile(source, dataDirectory+name)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidBundle, name)
	}
	var node yaml.Node
	if err := yaml.Unmarshal(body, &node); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrInvalidBundle, name, err)
	}
	if hasReferences(&node) {
		return fmt.Errorf("%w: якоря и ссылки в %s", ErrInvalidBundle, name)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(body))
	decoder.KnownFields(true)
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrInvalidBundle, name, err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: лишние данные в %s", ErrInvalidBundle, name)
	}
	return nil
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

func bundleChecksum(source fs.FS) (string, error) {
	hash := sha256.New()
	for _, name := range dataFiles {
		body, err := fs.ReadFile(source, dataDirectory+name)
		if err != nil {
			return "", err
		}
		_, _ = hash.Write([]byte(name + "\x00"))
		_, _ = hash.Write(body)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// PrepareManifest запрещает менять уже выпущенный номер при изменении данных.
func PrepareManifest(source fs.FS, version int64, write bool) (Manifest, error) {
	var current Manifest
	if err := decodeFile(source, "manifest.yaml", &current); err != nil {
		return Manifest{}, err
	}
	release, coverage, err := describe(source)
	if err != nil {
		return Manifest{}, err
	}
	if version == 0 {
		version = current.Release
	}
	next := Manifest{Release: version, Checksum: release.Checksum, Coverage: coverage}
	if version < 1 || version > taxonomy.MaxTaxonomyVersion {
		return next, ErrInvalidBundle
	}
	if next != current && (!write || version <= current.Release) {
		return next, ErrInvalidBundle
	}
	return next, nil
}
