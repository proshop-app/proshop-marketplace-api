package postgres

import (
	"regexp"
	"slices"
	"testing"
)

// Список упорядоченных видов в SQL взятия записан буквально (так планировщик выбирает частичный
// индекс головы полосы) и обязан совпадать с OrderedTaskKinds: вид, пропущенный в литерале, не
// нашёл бы голову полосы и молча перестал бы отдавать задачи.
func TestOrderedLaneKindsLiteralMatchesList(t *testing.T) {
	literal := []string{}
	for _, match := range regexp.MustCompile(`'([^']+)'`).FindAllStringSubmatch(orderedLaneKinds, -1) {
		literal = append(literal, match[1])
	}
	slices.Sort(literal)
	if !slices.Equal(literal, slices.Sorted(slices.Values(OrderedTaskKinds))) {
		t.Fatal(literal, OrderedTaskKinds)
	}
}
