package ports

import (
	"context"
	"errors"
)

// ErrIntervalTaken — интервал прохода уже взяла другая реплика: проход в этом интервале не нужен.
var ErrIntervalTaken = errors.New("интервал прохода уже взят другой репликой")

// IntervalWork — проход, который за общий интервал делает одна реплика установки.
type IntervalWork interface {
	// Run выполняет проход, если реплика взяла интервал; иначе — ErrIntervalTaken без прохода.
	Run(ctx context.Context, pass func(context.Context) error) error
}
