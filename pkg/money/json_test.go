package money_test

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestJSONPreservesLargeKopecks(t *testing.T) {
	original := money.FromKopecks(math.MaxInt64)
	raw, err := json.Marshal(original)
	if err != nil || string(raw) != "9223372036854775807" {
		t.Fatalf("копейки: %s %v", raw, err)
	}
	var restored money.Money
	if err := json.Unmarshal(raw, &restored); err != nil || restored != original {
		t.Fatalf("обратно: %v %v", restored, err)
	}
	for _, raw := range []string{`1.01`, `"100"`, `null`, `9223372036854775808`} {
		if err := json.Unmarshal([]byte(raw), &restored); err == nil {
			t.Fatalf("нецелые копейки: %s", raw)
		}
	}
}
