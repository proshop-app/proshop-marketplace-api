package moderation_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
)

func TestCaseSnapshotRevisionAndIsolation(t *testing.T) {
	now := time.Now()
	card := moderation.ReviewCard{
		Name:         "Товар",
		CategoryPath: []string{"Категория"},
		Attributes:   map[string]json.RawMessage{"a1": json.RawMessage(`"v1"`)},
	}
	v := moderation.VerdictState{TenantID: "t1", ProductID: "p1", Revision: 1, Status: moderation.Pending}
	c := moderation.NewCase("00000000-0000-4000-8000-000000000001", v, moderation.Rule, "", now, &card)
	c.MarkSaved()
	card.CategoryPath[0] = "Изменено"
	card.Attributes["a1"][1] = 'x'
	if c.State().Card.CategoryPath[0] != "Категория" || string(c.State().Card.Attributes["a1"]) != `"v1"` {
		t.Fatal("входная карточка изменила снимок")
	}
	read := c.State()
	read.Card.Name = "Изменено"
	restored := moderation.RestoreCase(c.State())
	if c.State().Card.Name != "Товар" || restored.State().Card.Name != "Товар" {
		t.Fatal("чтение изменило снимок")
	}
	card.Name = "Новая ревизия"
	if c.Refresh(v, now, &card) || c.State().Card.Name != "Товар" {
		t.Fatal("повтор ревизии заменил снимок")
	}
	v.Revision++
	if !c.Refresh(v, now, &card) || c.State().Card.Name != card.Name {
		t.Fatal("новая ревизия не обновила снимок")
	}
	if _, err := c.Decide(c.State().Version, v.Revision, moderation.Allow, "Заметка", now); err != nil {
		t.Fatal(err)
	}
	v.Revision++
	if c.Refresh(v, now, nil) || c.State().Card.Name != card.Name {
		t.Fatal("закрытое дело потеряло снимок")
	}
}
