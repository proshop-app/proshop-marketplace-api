package reference

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

const (
	importSchemaVersion   = 1
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

// ImportDictionary — поставка значений и вид создаваемого словаря.
type ImportDictionary struct {
	Code    string          `json:"code"`
	Kind    Kind            `json:"kind"`
	Release int64           `json:"release"`
	Entries []DeliveryEntry `json:"entries"`
}

// ImportPackage — один проверяемый документ нескольких словарей.
type ImportPackage struct {
	SchemaVersion int                `json:"schemaVersion"`
	Sources       []ImportSource     `json:"sources"`
	Dictionaries  []ImportDictionary `json:"dictionaries"`
}

// PreparedImport — неизменяемая поставка с проверенным происхождением.
type PreparedImport struct {
	Kind     Kind
	Delivery Delivery
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
		if codes[dictionary.Code] || (dictionary.Kind != KindOpen && dictionary.Kind != KindClosed) {
			return nil, ErrInvalidDelivery.WithMessage("повтор кода или неверный вид словаря")
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
	entries := slices.Clone(input.Entries)
	for i := range entries {
		if entries[i].Status == "" {
			entries[i].Status = StatusCandidate
		}
		metadata, err := importMetadata(entries[i].Metadata, sources)
		if err != nil {
			return PreparedImport{}, err
		}
		entries[i].Metadata = metadata
	}
	body, err := json.Marshal(entries)
	if err != nil {
		return PreparedImport{}, ErrInvalidDelivery.Wrap(err)
	}
	checksum := sha256.Sum256(body)
	delivery := Delivery{
		Dictionary: input.Code,
		Version:    input.Release,
		Checksum:   hex.EncodeToString(checksum[:]),
		Entries:    entries,
	}
	if err := ValidateDelivery(delivery); err != nil {
		return PreparedImport{}, err
	}
	return PreparedImport{Kind: input.Kind, Delivery: delivery}, nil
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
