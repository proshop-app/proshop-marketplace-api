package moderation_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
)

func TestKey(t *testing.T) {
	for text, want := range map[string]string{
		"  ТОВАР\t Ё\u0085\nТовар  ":   "товар е товар",
		"\u00a0Товар\u2003Товар\u202f": "товар товар",
		"":                             "",
	} {
		if got := moderation.Key(text); got != want {
			t.Errorf("ключ %q: %q, ожидался %q", text, got, want)
		}
	}
}

func TestRules(t *testing.T) {
	tests := []struct {
		name    string
		input   moderation.Input
		status  moderation.Status
		reasons []string
	}{
		{"чистая карточка", moderation.Input{Image: "image"}, moderation.Allowed, []string{}},
		{
			"запрет первым",
			moderation.Input{Policy: "forbidden", Name: "Товар", Probation: true},
			moderation.Rejected,
			[]string{"category_forbidden"},
		},
		{
			"ручная категория",
			moderation.Input{Policy: "manual", Image: "image"},
			moderation.Pending,
			[]string{"category_manual"},
		},
		{
			"фразы в двух полях",
			moderation.Input{Name: "ТОВАР", Description: "Магазин", Image: "image"},
			moderation.Pending,
			[]string{"stop_phrase", "stop_phrase"},
		},
		{
			"изображение останавливает",
			moderation.Input{Policy: "manual", Probation: true, ManualReject: true},
			moderation.Rejected,
			[]string{"category_manual", "image_missing"},
		},
		{
			"испытание и ручной отказ",
			moderation.Input{Probation: true, ManualReject: true, Image: "image"},
			moderation.Pending,
			[]string{"probation", "rejected_revision"},
		},
	}
	phrases := []moderation.StopPhrase{{Text: "Товар", Key: "товар"}, {Text: "Магазин", Key: "магазин"}}
	rules := moderation.Rules{Revision: 1, RequireImage: true, Phrases: phrases}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, reasons := rules.Evaluate(test.input)
			keys := []string{}
			for _, reason := range reasons {
				keys = append(keys, reason.Key[len("moderation."):])
			}
			if status != test.status || !reflect.DeepEqual(keys, test.reasons) {
				t.Fatalf("вердикт: %s %+v", status, reasons)
			}
		})
	}
	rules.RequireImage = false
	if status, _ := rules.Evaluate(moderation.Input{}); status != moderation.Allowed {
		t.Fatal(status)
	}
}

func TestVerdictRevisions(t *testing.T) {
	now := time.Now()
	v := moderation.NewVerdict("t1", "p1")
	rules := moderation.Rules{Revision: 1}
	if !v.Evaluate(1, 1, rules, moderation.Input{}, now) {
		t.Fatal("первая оценка")
	}
	v.MarkSaved()
	if v.Evaluate(1, 1, rules, moderation.Input{}, now) {
		t.Fatal("повтор изменил вердикт")
	}
	v.Decide("reject", "заметка", now)
	v.MarkSaved()
	rules.Revision++
	if v.Evaluate(1, 2, rules, moderation.Input{}, now) {
		t.Fatal("перезаписано решение оператора")
	}
	if !v.Evaluate(2, 2, rules, moderation.Input{}, now) || v.State().Status != moderation.Pending {
		t.Fatal("ручной отказ должен прилипать")
	}
	v.MarkSaved()
	v.Decide("allow", "", now)
	v.MarkSaved()
	if !v.Evaluate(3, 2, rules, moderation.Input{Probation: true}, now) || v.Allowed() {
		t.Fatal("ручное одобрение действует только на одну ревизию")
	}
	v.MarkSaved()
	if !v.Evaluate(3, 2, rules, moderation.Input{}, now) || !v.Allowed() {
		t.Fatal("смена доверия")
	}
	if v.Evaluate(2, 2, rules, moderation.Input{}, now) {
		t.Fatal("старая ревизия")
	}
	state := v.State()
	state.Reasons = append(state.Reasons, moderation.Reason{Key: "изменение копии"})
	if len(v.State().Reasons) != 0 {
		t.Fatal("утечка состояния")
	}
}

func TestCase(t *testing.T) {
	now := time.Now()
	v := moderation.NewVerdict("t1", "p1")
	v.Evaluate(1, 1, moderation.Rules{}, moderation.Input{Probation: true}, now)
	c := moderation.NewCase("00000000-0000-4000-8000-000000000001", v.State(), moderation.Rule, "", now, nil)
	c.MarkSaved()
	if _, err := c.Decide(0, 1, "allow", "заметка", now); !errors.Is(err, moderation.ErrCaseConflict) {
		t.Fatal(err)
	}
	if _, err := c.Decide(1, 2, "allow", "заметка", now); !errors.Is(err, moderation.ErrCaseStale) {
		t.Fatal(err)
	}
	if _, err := c.Decide(1, 1, "allow", "  ", now); !errors.Is(err, moderation.ErrInvalid) {
		t.Fatal(err)
	}
	if changed, err := c.Decide(1, 1, "allow", " заметка ", now); err != nil || !changed {
		t.Fatal(err)
	}
	if c.State().Note != "заметка" || c.State().Status != moderation.Allowed {
		t.Fatal(c.State())
	}
}

func TestDictionaries(t *testing.T) {
	phrase, err := moderation.NewStopPhrase("  ТОВАР\tЁ  ", moderation.Actor{}, time.Now())
	if err != nil || phrase.Text != "ТОВАР Ё" || phrase.Key != "товар е" {
		t.Fatal(phrase, err)
	}
	if _, err := moderation.NewStopPhrase("я", moderation.Actor{}, time.Now()); err == nil {
		t.Fatal("короткая фраза")
	}
	if err := (moderation.ReasonCode{Code: "r1", Text: "Причина"}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (moderation.ReasonCode{Code: "R1", Text: "Причина"}).Validate(); err == nil {
		t.Fatal("неверный код")
	}
}
