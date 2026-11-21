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

type artifactAttributes []taxonomy.Attribute

type artifactAttribute struct {
	taxonomy.Attribute
	Values             json.RawMessage `json:"values,omitempty"`
	FacetOptions       json.RawMessage `json:"facetOptions,omitempty"`
	FacetValuesByValue json.RawMessage `json:"facetValuesByValue,omitempty"`
}

type artifactDefinition struct {
	Attributes         artifactAttributes           `json:"attributes"`
	Schemas            map[string]taxonomy.Schema   `json:"schemas"`
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

func (attributes *artifactAttributes) UnmarshalJSON(body []byte) error {
	var fields []artifactAttribute
	if err := decodeStrictJSON(body, &fields); err != nil {
		return err
	}
	decoded := make(artifactAttributes, 0, len(fields))
	for _, field := range fields {
		// Значения и фасетные списки появляются при публикации из реестра.
		if field.Values != nil || field.FacetOptions != nil || field.FacetValuesByValue != nil {
			return fmt.Errorf("атрибут %s: вычисляемое поле в исходнике", field.Code)
		}
		for _, meta := range field.ValuesMeta {
			if meta.Order != nil {
				return fmt.Errorf("атрибут %s: порядок значений задаётся в реестре", field.Code)
			}
		}
		decoded = append(decoded, field.Attribute)
	}
	*attributes = decoded

	return nil
}

func prepareArtifact(bundle artifact, digest string) (taxonomy.Release, Coverage, error) {
	release := taxonomy.Release{
		Version:            bundle.Manifest.Release,
		Checksum:           digest,
		ReplaceCategories:  bundle.Manifest.ReplaceCategories,
		Attributes:         bundle.Attributes,
		Schemas:            bundle.Schemas,
		Categories:         bundle.Categories,
		DictionaryBindings: bundle.DictionaryBindings,
	}
	// Исторический идентификатор поставки берётся из проверенного манифеста.
	if bundle.Manifest.Checksum != "" {
		release.Checksum = bundle.Manifest.Checksum
	}
	schemas, err := release.Definition().ExpandSchemaSource()
	if err != nil {
		return taxonomy.Release{}, Coverage{}, err
	}
	release.Schemas = schemas
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
