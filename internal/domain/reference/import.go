package reference

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

const (
	importSchemaVersion   = 2
	maxImportDictionaries = 1000
	maxImportEntries      = 100000
	maxImportSources      = 10000
)

var importSourceID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:-]{0,127}$`)

// ImportSource — происхождение данных, а не подтверждение свойств товара.
type ImportSource struct {
	ID      string `json:"id"`
	URL     string `json:"url"`
	License string `json:"license"`
	Version string `json:"version,omitempty"`
}

// ImportDictionary — полный заголовок и записи одного выпуска словаря.
type ImportDictionary struct {
	Code       string          `json:"code"`
	Kind       Kind            `json:"kind"`
	Identity   bool            `json:"identity"`
	AliasKinds []AliasKind     `json:"aliasKinds"`
	Limits     Limits          `json:"limits"`
	Release    int64           `json:"release"`
	Entries    []DeliveryEntry `json:"entries"`
}

// ImportPackage — один проверяемый документ нескольких словарей.
type ImportPackage struct {
	SchemaVersion int                `json:"schemaVersion"`
	Sources       []ImportSource     `json:"sources"`
	Dictionaries  []ImportDictionary `json:"dictionaries"`
}

// PreparedImport — неизменяемая поставка с проверенным происхождением.
type PreparedImport struct {
	Settings DictionarySettings
	Delivery Delivery
}

type importDictionaryFields ImportDictionary

type importDictionaryJSON struct {
	*importDictionaryFields
	Identity *bool `json:"identity"`
}

// UnmarshalJSON требует явного identity, не меняя тип настройки в агрегате.
func (input *ImportDictionary) UnmarshalJSON(body []byte) error {
	var fields importDictionaryFields
	decoded := importDictionaryJSON{importDictionaryFields: &fields}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	if decoded.Identity == nil {
		return ErrInvalidSettings.WithMessage("identity — обязательный boolean")
	}
	fields.Identity = *decoded.Identity
	*input = ImportDictionary(fields)
	return nil
}

// PrepareImport проверяет весь пакет до транзакции и считает суммы после
// обогащения metadata. Порядок ключей JSON и формат JSON/YAML сумму не меняют.
func PrepareImport(input ImportPackage) ([]PreparedImport, error) {
	if input.SchemaVersion != importSchemaVersion || len(input.Sources) == 0 ||
		len(input.Sources) > maxImportSources || len(input.Dictionaries) == 0 ||
		len(input.Dictionaries) > maxImportDictionaries {
		return nil, ErrInvalidDelivery.WithMessage("неверная версия или состав пакета")
	}
	sources, err := importSources(input.Sources)
	if err != nil {
		return nil, err
	}
	prepared := make([]PreparedImport, 0, len(input.Dictionaries))
	codes := map[string]bool{}
	total := 0
	for _, dictionary := range input.Dictionaries {
		if codes[dictionary.Code] {
			return nil, ErrInvalidDelivery.WithMessage("повтор кода словаря")
		}
		codes[dictionary.Code] = true
		total += len(dictionary.Entries)
		if total > maxImportEntries {
			return nil, ErrLimit.WithMessage("в пакете не больше 100000 записей")
		}
		item, err := prepareDictionary(dictionary, sources)
		if err != nil {
			return nil, err
		}
		prepared = append(prepared, item)
	}
	slices.SortFunc(prepared, func(a, b PreparedImport) int {
		return strings.Compare(a.Delivery.Dictionary, b.Delivery.Dictionary)
	})
	return prepared, nil
}

func importSources(input []ImportSource) (map[string]ImportSource, error) {
	sources := map[string]ImportSource{}
	for _, source := range input {
		location, err := url.Parse(source.URL)
		if !importSourceID.MatchString(source.ID) || sources[source.ID].ID != "" || err != nil ||
			location.Scheme != "https" || location.Hostname() == "" || location.User != nil ||
			len(source.URL) > 2048 || strings.TrimSpace(source.License) == "" || len(source.License) > 256 ||
			len(source.Version) > 256 {
			return nil, ErrInvalidDelivery.WithMessage("неверный или повторный источник пакета")
		}
		sources[source.ID] = source
	}
	return sources, nil
}

func prepareDictionary(input ImportDictionary, sources map[string]ImportSource) (PreparedImport, error) {
	if input.Entries == nil {
		return PreparedImport{}, ErrInvalidDelivery.WithMessage("entries — обязательный массив, не null")
	}
	settings := input.settings()
	if err := settings.Validate(); err != nil {
		return PreparedImport{}, err
	}
	settings.AliasKinds = slices.Clone(settings.AliasKinds)
	slices.Sort(settings.AliasKinds)
	entries, err := prepareImportEntries(input.Entries, sources)
	if err != nil {
		return PreparedImport{}, err
	}
	checksum, err := importChecksum(settings, entries)
	if err != nil {
		return PreparedImport{}, err
	}
	delivery := Delivery{
		Dictionary: input.Code,
		Version:    input.Release,
		Checksum:   checksum,
		Entries:    entries,
	}
	dictionary := RestoreDictionary(DictionaryState{
		Code:       input.Code,
		Kind:       settings.Kind,
		Identity:   settings.Identity,
		AliasKinds: settings.AliasKinds,
		Limits:     settings.Limits,
	})
	if err := validateDelivery(delivery, dictionary, input.Kind == KindOpen); err != nil {
		return PreparedImport{}, err
	}
	return PreparedImport{Settings: settings, Delivery: delivery}, nil
}

func importMetadata(raw json.RawMessage, sources map[string]ImportSource) (json.RawMessage, error) {
	clean, err := CleanMetadata(raw)
	if err != nil {
		return nil, err
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(clean, &fields); err != nil {
		return nil, ErrInvalidDelivery.Wrap(err)
	}
	var sourceID string
	if value, ok := fields["sourceId"]; ok {
		if err := json.Unmarshal(value, &sourceID); err != nil {
			return nil, ErrInvalidDelivery.WithMessage("metadata.sourceId — строка")
		}
	} else if len(sources) == 1 {
		for id := range sources {
			sourceID = id
		}
	}
	source, ok := sources[sourceID]
	if !ok {
		return nil, ErrInvalidDelivery.WithMessage("источник записи не объявлен в sources")
	}
	provenance := map[string]string{
		"sourceId":      source.ID,
		"sourceUrl":     source.URL,
		"sourceLicense": source.License,
	}
	delete(fields, "sourceVersion")
	if source.Version != "" {
		provenance["sourceVersion"] = source.Version
	}
	for key, value := range provenance {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, ErrInvalidDelivery.Wrap(err)
		}
		fields[key] = encoded
	}
	body, err := json.Marshal(fields)
	if err != nil {
		return nil, ErrInvalidDelivery.Wrap(err)
	}
	return CleanMetadata(body)
}

// settings возвращает заголовок, включённый в контрольную сумму выпуска.
func (input ImportDictionary) settings() DictionarySettings {
	return DictionarySettings{
		Kind:       input.Kind,
		Identity:   input.Identity,
		AliasKinds: input.AliasKinds,
		Limits:     input.Limits,
	}
}

func prepareImportEntries(input []DeliveryEntry, sources map[string]ImportSource) ([]DeliveryEntry, error) {
	entries := slices.Clone(input)
	for i := range entries {
		if entries[i].Status == "" {
			entries[i].Status = StatusCandidate
		}
		metadata, err := importMetadata(entries[i].Metadata, sources)
		if err != nil {
			return nil, err
		}
		entries[i].Metadata = metadata
	}
	return entries, nil
}

func importChecksum(settings DictionarySettings, entries []DeliveryEntry) (string, error) {
	body, err := json.Marshal(struct {
		Settings DictionarySettings `json:"settings"`
		Entries  []DeliveryEntry    `json:"entries"`
	}{
		Settings: settings,
		Entries:  entries,
	})
	if err != nil {
		return "", ErrInvalidDelivery.Wrap(err)
	}
	checksum := sha256.Sum256(body)
	return hex.EncodeToString(checksum[:]), nil
}
