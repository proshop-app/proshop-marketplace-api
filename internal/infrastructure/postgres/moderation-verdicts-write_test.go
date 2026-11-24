package postgres

import (
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
)

// Пустые причины и решение пишутся текстом "null" и становятся jsonb null — значением, которое
// принимает колонка NOT NULL reasons. JSON null набора записей стал бы SQL NULL и откатил пачку.
func TestVerdictWritesKeepJSONNull(t *testing.T) {
	verdict := moderation.RestoreVerdict(moderation.VerdictState{TenantID: "t1", ProductID: "p1"})
	writes, err := verdictWritesOf([]*moderation.Verdict{verdict})
	if err != nil || writes.reasons[0] != "null" || writes.decisions[0] != "null" {
		t.Fatal(writes.reasons, writes.decisions, err)
	}
}
