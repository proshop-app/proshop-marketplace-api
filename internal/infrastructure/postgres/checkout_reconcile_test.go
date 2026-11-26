package postgres

import (
	"strings"
	"testing"
)

func TestReconcileRevisionRejectsDamagedJSON(t *testing.T) {
	for _, raw := range []string{"", "null", `"5"`, `"сломано"`, "1.5", "-1", "9223372036854775808"} {
		if _, err := reconcileRevision([]byte(raw)); err == nil || !strings.Contains(err.Error(), "OrderRevision") {
			t.Errorf("ревизия %q: %v", raw, err)
		}
	}
	for _, tc := range []struct {
		raw  string
		want int64
	}{
		{raw: "0", want: 0},
		{raw: "9223372036854775807", want: 9223372036854775807},
	} {
		if got, err := reconcileRevision([]byte(tc.raw)); err != nil || got != tc.want {
			t.Errorf("ревизия %q: %d, %v", tc.raw, got, err)
		}
	}
}
