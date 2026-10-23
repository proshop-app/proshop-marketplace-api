package fake

import "context"

// OrderRecovery — наблюдение запусков фоновой сверки без настоящих магазинов.
type OrderRecovery struct{ Calls chan struct{} }

// Resume отмечает один проход, пока контекст не отменён.
func (r *OrderRecovery) Resume(ctx context.Context) error {
	select {
	case r.Calls <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
