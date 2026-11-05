package moderationstatus_test

import (
	"reflect"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/moderationstatus"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestStatus(t *testing.T) {
	for _, published := range []bool{false, true} {
		statuses := []moderation.Status{"", moderation.Pending, moderation.Allowed, moderation.Rejected}
		for _, verdictStatus := range statuses {
			publication := taxonomy.Publication{Published: published}
			expected := []moderationstatus.Reason{}
			if !published {
				publication.Issues = []taxonomy.Issue{{Code: "taxonomy.required", Attribute: "a1"}}
				expected = append(expected, moderationstatus.Reason{
					Source:  "taxonomy",
					Key:     "taxonomy.required",
					Message: "Карточка не прошла проверку структуры: a1",
				})
			}
			var verdict *moderation.Verdict
			reason := moderation.NotEvaluated()
			if verdictStatus != "" {
				reason = moderation.Reason{Key: "moderation.manual_review", Message: "Проверка"}
				verdict = moderation.RestoreVerdict(moderation.VerdictState{
					Status:  verdictStatus,
					Reasons: []moderation.Reason{reason},
				})
			}
			expected = append(expected, moderationstatus.Reason{
				Source:  "moderation",
				Key:     reason.Key,
				Message: reason.Message,
			})
			want := "hidden"
			if verdictStatus == moderation.Pending {
				want = "pending"
			} else if published && verdictStatus == moderation.Allowed {
				want, expected = "published", []moderationstatus.Reason{}
			}
			status, reasons := moderationstatus.Status(publication, verdict)
			if status != want || !reflect.DeepEqual(reasons, expected) {
				t.Fatalf("публикация %v, вердикт %s: %s, %+v", published, verdictStatus, status, reasons)
			}
		}
	}
}
