package taxonomydata

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

const artifactSchemaVersion = 1

var (
	//go:embed release.lock.json
	artifactLock []byte
	//go:embed taxonomy-bundle.json
	artifactBytes []byte
)

type releaseLock struct {
	Release int64  `json:"release"`
	SHA256  string `json:"sha256"`
}

type artifactManifest struct {
	ReplaceCategories bool     `json:"replaceCategories,omitempty"`
	Release           int64    `json:"release"`
	Checksum          string   `json:"checksum,omitempty"`
	Coverage          Coverage `json:"coverage"`
}

type artifactAttribute struct {
	taxonomy.Attribute
	FacetPinned        json.RawMessage `json:"facetPinned,omitempty"`
	Values             json.RawMessage `json:"values,omitempty"`
	FacetOptions       json.RawMessage `json:"facetOptions,omitempty"`
	FacetValuesByValue json.RawMessage `json:"facetValuesByValue,omitempty"`
}

type artifactSchema struct {
	taxonomy.Schema
	HiddenFacets json.RawMessage `json:"hiddenFacets,omitempty"`
	Filters      json.RawMessage `json:"filters,omitempty"`
}

type artifactDefinition struct {
	Attributes         []artifactAttribute          `json:"attributes"`
	Schemas            map[string]artifactSchema    `json:"schemas"`
	Categories         []taxonomy.Category          `json:"categories"`
	DictionaryBindings []taxonomy.DictionaryBinding `json:"dictionaryBindings"`
}

type migrationSource struct {
	artifactDefinition
	Version int64 `json:"version"`
}

type artifact struct {
	artifactDefinition
	SchemaVersion      int                `json:"schemaVersion"`
	Manifest           artifactManifest   `json:"manifest"`
	CategoryMigration  *categoryMigration `json:"categoryMigration"`
	MigrationSource    *migrationSource   `json:"migrationSource"`
	SourceCommit       string             `json:"sourceCommit"`
	DictionaryReleases map[string]int64   `json:"dictionaryReleases"`
}

// Load проверяет встроенный JSON-артефакт до подключения к БД.
func Load() (taxonomy.Release, Coverage, error) {
	var lock releaseLock
	if err := json.Unmarshal(artifactLock, &lock); err != nil {
		return taxonomy.Release{}, Coverage{}, fmt.Errorf("%w: закрепление выпуска: %w", ErrInvalidBundle, err)
	}

	return loadArtifact(artifactBytes, lock)
}

func loadArtifact(body []byte, lock releaseLock) (taxonomy.Release, Coverage, error) {
	if fmt.Sprintf("%x", sha256.Sum256(body)) != lock.SHA256 {
		return taxonomy.Release{}, Coverage{}, fmt.Errorf("%w: SHA-256 артефакта не совпадает", ErrInvalidBundle)
	}
	bundle, err := decodeArtifact(body)
	if err != nil {
		return taxonomy.Release{}, Coverage{}, err
	}
	if bundle.Manifest.Release != lock.Release {
		return taxonomy.Release{}, Coverage{}, fmt.Errorf("%w: ожидался выпуск %d", ErrInvalidBundle, lock.Release)
	}

	return prepareArtifact(bundle, lock.SHA256)
}

func decodeArtifact(body []byte) (artifact, error) {
	var bundle artifact
	if err := decodeStrictJSON(body, &bundle); err != nil {
		return artifact{}, fmt.Errorf("%w: %w", ErrInvalidBundle, err)
	}
	if bundle.SchemaVersion != artifactSchemaVersion {
		return artifact{}, fmt.Errorf("%w: версия формата %d не поддерживается", ErrInvalidBundle, bundle.SchemaVersion)
	}
	if bundle.DictionaryBindings == nil || bundle.DictionaryReleases == nil || bundle.SourceCommit == "" {
		return artifact{}, fmt.Errorf("%w: отсутствуют привязки или сведения об источнике", ErrInvalidBundle)
	}

	return bundle, nil
}

func decodeStrictJSON(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return fmt.Errorf("лишние данные после JSON: %w", ErrInvalidBundle)
	}

	return nil
}

func (source artifactDefinition) definition(version int64) (taxonomy.Definition, error) {
	legacy := version <= 16
	d := taxonomy.Definition{
		Version:            version,
		Attributes:         make([]taxonomy.Attribute, 0, len(source.Attributes)),
		Schemas:            make(map[string]taxonomy.Schema, len(source.Schemas)),
		Categories:         source.Categories,
		DictionaryBindings: source.DictionaryBindings,
	}
	for _, field := range source.Attributes {
		// Значения и фасетные списки появляются при публикации из реестра.
		if field.Values != nil || field.FacetOptions != nil || field.FacetValuesByValue != nil {
			return taxonomy.Definition{}, fmt.Errorf("атрибут %s: вычисляемое поле в исходнике", field.Code)
		}
		if field.FacetPinned != nil {
			var pinned bool
			if !legacy || json.Unmarshal(field.FacetPinned, &pinned) != nil {
				return taxonomy.Definition{}, fmt.Errorf("атрибут %s: недопустимый facetPinned", field.Code)
			}
		}
		for _, meta := range field.ValuesMeta {
			if meta.Order != nil {
				return taxonomy.Definition{}, fmt.Errorf("атрибут %s: порядок значений задаётся в реестре", field.Code)
			}
		}
		d.Attributes = append(d.Attributes, field.Attribute)
	}
	for name, recipe := range source.Schemas {
		schema, err := recipe.source(name, legacy)
		if err != nil {
			return taxonomy.Definition{}, err
		}
		d.Schemas[name] = schema
	}
	return d, validateCategoryFacetSource(source.Categories, legacy)
}

func (recipe artifactSchema) source(name string, legacy bool) (taxonomy.Schema, error) {
	schema := recipe.Schema
	if recipe.Filters != nil || legacy && (schema.FacetOverrides != nil || schema.FacetLayout != nil) {
		return taxonomy.Schema{}, fmt.Errorf("схема %s: недопустимые поля фильтров", name)
	}
	if recipe.HiddenFacets == nil {
		return schema, nil
	}
	var hidden []string
	if !legacy || json.Unmarshal(recipe.HiddenFacets, &hidden) != nil {
		return taxonomy.Schema{}, fmt.Errorf("схема %s: недопустимый hiddenFacets", name)
	}
	schema.FacetOverrides = make(map[string]taxonomy.FacetOverride, len(hidden))
	for _, code := range hidden {
		if _, exists := schema.FacetOverrides[code]; exists {
			return taxonomy.Schema{}, fmt.Errorf("схема %s: повтор hiddenFacets %s", name, code)
		}
		schema.FacetOverrides[code] = taxonomy.FacetOff
	}
	return schema, nil
}

func validateCategoryFacetSource(nodes []taxonomy.Category, legacy bool) error {
	for _, node := range nodes {
		if node.PriorityFilters != nil || legacy && node.FacetLayout != nil {
			return fmt.Errorf("категория %s: недопустимые поля фильтров", node.ID)
		}
		if err := validateCategoryFacetSource(node.Children, legacy); err != nil {
			return err
		}
	}
	return nil
}

func prepareArtifact(bundle artifact, digest string) (taxonomy.Release, Coverage, error) {
	d, err := bundle.definition(bundle.Manifest.Release)
	if err != nil {
		return taxonomy.Release{}, Coverage{}, fmt.Errorf("%w: %w", ErrInvalidBundle, err)
	}
	d.Schemas, err = d.ExpandSchemaSource()
	if err != nil {
		return taxonomy.Release{}, Coverage{}, err
	}
	release := taxonomy.Release{
		Version:            d.Version,
		Checksum:           digest,
		ReplaceCategories:  bundle.Manifest.ReplaceCategories,
		Attributes:         d.Attributes,
		Schemas:            d.Schemas,
		Categories:         d.Categories,
		DictionaryBindings: d.DictionaryBindings,
	}
	// Исторический идентификатор поставки берётся из проверенного манифеста.
	if bundle.Manifest.Checksum != "" {
		release.Checksum = bundle.Manifest.Checksum
	}
	coverage, err := checkCoverage(release)
	if err != nil {
		return taxonomy.Release{}, Coverage{}, err
	}
	if coverage != bundle.Manifest.Coverage {
		return taxonomy.Release{}, Coverage{}, fmt.Errorf("%w: охват не совпадает с манифестом", ErrInvalidBundle)
	}
	if err := validateArtifactMigration(bundle, release); err != nil {
		return taxonomy.Release{}, Coverage{}, err
	}

	d, err = d.CompileFacets(d.Version >= 17)
	if err != nil {
		return taxonomy.Release{}, Coverage{}, err
	}
	release.Schemas = d.Schemas
	release.Categories = d.Categories
	release.FacetKeys = d.FacetKeys
	release.RootFilters = d.RootFilters
	return release, coverage, nil
}

func validateArtifactMigration(bundle artifact, release taxonomy.Release) error {
	migration, source := bundle.CategoryMigration, bundle.MigrationSource
	if !release.ReplaceCategories && migration == nil && source == nil {
		return nil
	}
	if !release.ReplaceCategories || migration == nil || source == nil ||
		migration.ToRelease != release.Version || migration.FromRelease != source.Version {
		return fmt.Errorf("%w: несогласованные версии миграции категорий", ErrInvalidBundle)
	}

	return validateCategoryMigration(*migration, source.Categories, release.Categories)
}
