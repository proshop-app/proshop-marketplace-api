package reference

import (
	"encoding/json"

	"github.com/proshop-app/proshop-marketplace/internal/domain/colorpalette"
)

// ValidateColorState проверяет привязку итогового образа перед записью журнала.
func ValidateColorState(dict *Dictionary, state EntryState) error {
	if dict.Code() != colorpalette.Dictionary {
		return nil
	}
	parents, err := colorpalette.Parents(state.Metadata)
	if err != nil {
		return err
	}
	return colorpalette.Validate(state.CanonicalName, parents, state.Status == StatusConfirmed)
}

// DeliveryMetadata дополняет привязку, не затирая другие поля и выбор оператора.
func (e *Entry) DeliveryMetadata(raw json.RawMessage) (bool, error) {
	metadata := e.Metadata()
	if string(metadata) == "{}" {
		if len(raw) == 0 {
			return false, nil
		}
		return e.SetMetadata(raw)
	}
	if e.state.Dictionary != colorpalette.Dictionary {
		return false, nil
	}
	current, err := colorpalette.Parents(metadata)
	if err != nil || len(current) > 0 {
		return false, err
	}
	incoming, err := colorpalette.Parents(raw)
	if err != nil || len(incoming) == 0 {
		return false, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(metadata, &fields); err != nil {
		return false, ErrInvalid.Wrap(err)
	}
	fields["baseColors"], err = json.Marshal(incoming)
	if err != nil {
		return false, err
	}
	metadata, err = json.Marshal(fields)
	if err != nil {
		return false, err
	}
	return e.SetMetadata(metadata)
}
