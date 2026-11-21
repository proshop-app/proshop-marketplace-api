package main

import (
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/application/searchfeedback"
)

const (
	searchPurchaseWorkers = 4
	searchPurchaseLease   = 10 * time.Second
)

func searchPurchaseDispatcher(
	queue ports.TaskQueue,
	clock ports.Clock,
	sink ports.SearchPurchaseSink,
) *messaging.Dispatcher {
	purchases := searchfeedback.NewPurchases(sink, clock)
	return messaging.NewDispatcher(
		queue,
		clock,
		messaging.DispatcherConfig{
			Kinds:          []string{searchfeedback.TaskPurchase},
			Batch:          searchPurchaseWorkers,
			PerTenant:      1,
			Lease:          searchPurchaseLease,
			Concurrency:    searchPurchaseWorkers,
			HandlerTimeout: searchfeedback.PurchaseCallTimeout,
		},
		map[string]messaging.Handler{searchfeedback.TaskPurchase: purchases.Deliver},
	)
}
