package fake

import (
	"context"
	"sort"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/event"
)

func (p parked) Get(_ context.Context, id int64) (ports.ParkedEvent, error) {
	for _, value := range p.d.parked {
		if value.Control.ID == id {
			return value, nil
		}
	}
	return ports.ParkedEvent{}, event.ErrNotFound
}

func (p parked) List(_ context.Context, f ports.EventFilter) ([]ports.ParkedEvent, int, error) {
	items := []ports.ParkedEvent{}
	for _, value := range p.d.parked {
		if (f.TenantID == "" || f.TenantID == value.TenantID) &&
			(f.Status == "" || f.Status == value.Control.Status) &&
			(f.Topic == "" || f.Topic == value.Topic) && (f.Reason == "" || f.Reason == value.Reason) {
			items = append(items, value)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Control.ID > items[j].Control.ID })
	total := len(items)
	start := min(total, f.Offset)
	return items[start:min(total, start+f.Limit)], total, nil
}

func (p parked) Update(_ context.Context, aggregate *event.Event, failure string, attempts int) error {
	for key, value := range p.d.parked {
		if value.Control.ID != aggregate.State().ID {
			continue
		}
		if value.Control.Version != aggregate.Origin().Version || value.Control.Status != aggregate.Origin().Status {
			return event.ErrConflict
		}
		value.Control = aggregate.State()
		if aggregate.Origin().Status == event.Queued {
			value.Attempts += attempts
			value.Error = failure
		}
		p.d.parked[key] = value
		aggregate.MarkSaved()
		return nil
	}
	return event.ErrConflict
}
