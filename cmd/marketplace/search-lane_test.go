package main

import (
	"slices"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/searchfeedback"
)

// Покупка для ранжирования зовёт поиск: её ведёт полоса вызовов поиска общего цикла,
// а не отдельный цикл со своим ежесекундным опросом очереди.
func TestSearchPurchaseRidesSearchLane(t *testing.T) {
	if !slices.Contains(searchLaneKinds, searchfeedback.TaskPurchase) {
		t.Fatal("покупка поиска вне полосы вызовов поиска", searchLaneKinds)
	}
}
