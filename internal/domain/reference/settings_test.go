package reference

import (
	"reflect"
	"testing"
)

func TestConfigureSameSettingsDoesNotChangeState(t *testing.T) {
	state := DictionaryState{
		Code:       "d1",
		Kind:       KindOpen,
		Identity:   true,
		AliasKinds: []AliasKind{AliasSpelling, AliasLocale},
		Limits:     DefaultLimits(),
	}
	dictionary := RestoreDictionary(state)
	settings := DictionarySettings{
		Kind:       state.Kind,
		Identity:   state.Identity,
		AliasKinds: []AliasKind{AliasLocale, AliasSpelling},
		Limits:     state.Limits,
	}
	changed, err := dictionary.Configure(settings)
	if err != nil || changed || !reflect.DeepEqual(dictionary.State(), state) {
		t.Fatal("повтор настроек изменил состояние", changed, err, dictionary.State())
	}
	settings.Limits.MaxEntries--
	changed, err = dictionary.Configure(settings)
	if err != nil || !changed {
		t.Fatal("новые настройки не изменили состояние", changed, err)
	}
	changed, err = dictionary.Configure(settings)
	if err != nil || changed {
		t.Fatal("повтор настроек изменил состояние", changed, err)
	}
}
