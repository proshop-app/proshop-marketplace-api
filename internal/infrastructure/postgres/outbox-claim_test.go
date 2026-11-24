package postgres

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// Голова упорядоченной полосы читается частичным индексом: список видов в запросе, в предикате
// индекса и в OrderedTaskKinds обязан совпадать, иначе вид не будет взят никогда.
func TestOrderedLaneKindsMatchIndex(t *testing.T) {
	quoted := make([]string, 0, len(OrderedTaskKinds))
	for _, kind := range OrderedTaskKinds {
		quoted = append(quoted, fmt.Sprintf("'%s'", kind))
	}
	want := strings.Join(quoted, ", ")
	if strings.Join(strings.Fields(orderedLaneKinds), " ") != "("+want+")" {
		t.Fatalf("виды запроса %s, ожидались (%s)", orderedLaneKinds, want)
	}
	migration, err := os.ReadFile("migrations/00070_outbox_lanes.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(strings.Fields(string(migration)), " "), "kind IN ("+want+")") {
		t.Fatal("предикат outbox_ordered_head_idx расходится с OrderedTaskKinds")
	}
}
