package main

import (
	"slices"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

// Упорядоченный вид без индекса головы полосы взятие отвергает: список диспетчера совпадает с ним.
func TestOrderedTaskKindsHaveLaneIndex(t *testing.T) {
	dispatcher := slices.Sorted(slices.Values(orderedTaskKinds))
	if !slices.Equal(dispatcher, slices.Sorted(slices.Values(postgres.OrderedTaskKinds))) {
		t.Fatal(orderedTaskKinds, postgres.OrderedTaskKinds)
	}
}
