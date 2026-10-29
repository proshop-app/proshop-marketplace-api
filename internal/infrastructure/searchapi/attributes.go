package searchapi

import (
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Тип передаётся явно: строки «001» и «true» не становятся числом или флагом.
func encodeAttributes(values map[string]json.RawMessage) ([]attribute, error) {
	keys := make([]string, 0, len(values))

	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	out := make([]attribute, 0, len(keys))

	for _, key := range keys {
		var value any
		decoder := json.NewDecoder(strings.NewReader(string(values[key])))
		decoder.UseNumber()

		if err := decoder.Decode(&value); err != nil {
			return nil, ErrInvalid.Wrap(err)
		}

		field := attribute{
			Key: key,
		}

		switch scalar := value.(type) {
		case string:
			field.Value = scalar
		case bool:
			field.Type = "boolean"
			field.Value = strconv.FormatBool(scalar)
		case json.Number:
			number, err := scalar.Float64()

			if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
				return nil, ErrInvalid
			}
			field.Type = "number"
			field.Value = scalar.String()
			field.NumericValue = &number
		default:
			return nil, ErrInvalid.WithMessage("характеристика не является проверенным скаляром")
		}

		out = append(out, field)
	}

	return out, nil
}

func decodeAttributes(values []attribute) (map[string]json.RawMessage, error) {
	out := make(map[string]json.RawMessage, len(values))

	for _, field := range values {
		if _, exists := out[field.Key]; exists {
			return nil, ErrUnavailable.WithMessage("повтор характеристики в ответе поиска")
		}

		var raw []byte

		switch field.Type {
		case "":
			raw, _ = json.Marshal(field.Value)
		case "boolean":
			if field.Value != "true" && field.Value != "false" {
				return nil, ErrUnavailable
			}
			raw = []byte(field.Value)
		case "number":
			value, err := strconv.ParseFloat(field.Value, 64)

			if !json.Valid([]byte(field.Value)) || err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
				return nil, ErrUnavailable
			}

			raw = []byte(field.Value)
		default:
			return nil, ErrUnavailable.WithMessage("неизвестный тип характеристики в ответе поиска")
		}

		out[field.Key] = raw
	}

	return out, nil
}
