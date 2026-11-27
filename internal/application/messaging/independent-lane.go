package messaging

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/safego"
)

// IndependentLane отделяет выполнение медленных видов от общего прохода.
// Один цикл берёт работу для обеих полос, пока независимая полоса свободна.
type IndependentLane struct {
	dispatcher    *Dispatcher
	gate          ports.TaskLaneGate
	pause         time.Duration
	shouldProbe   func(error) bool
	confirmOutage func(context.Context) bool
	done          chan struct{}
	outcomes      []Outcome
	err           error
}

// NewIndependentLane создаёт полосу без собственного опроса очереди.
func NewIndependentLane(
	dispatcher *Dispatcher,
	gate ports.TaskLaneGate,
	pause time.Duration,
	shouldProbe func(error) bool,
	confirmOutage func(context.Context) bool,
) *IndependentLane {
	return &IndependentLane{
		dispatcher:    dispatcher,
		gate:          gate,
		pause:         pause,
		shouldProbe:   shouldProbe,
		confirmOutage: confirmOutage,
	}
}

// RunOnce вызывается последовательно одним циклом. Пока отдельная полоса занята,
// общая продолжает брать и подтверждать свои задачи.
func (l *IndependentLane) RunOnce(ctx context.Context) ([]Outcome, error) {
	var previous []Outcome
	var previousErr error
	select {
	case <-l.done:
		previous, previousErr = l.outcomes, l.err
		l.done, l.outcomes, l.err = nil, nil, nil
	default:
	}
	gate := l.gate
	var exclude []string
	if l.done != nil {
		exclude = gate.Kinds
		gate = ports.TaskLaneGate{}
	}
	tasks, claimErr := l.dispatcher.claimPass(ctx, gate, exclude)
	var detached, shared []ports.Task
	for _, task := range tasks {
		if slices.Contains(l.gate.Kinds, task.Kind) {
			detached = append(detached, task)
		} else {
			shared = append(shared, task)
		}
	}
	if len(detached) > 0 {
		l.start(ctx, detached)
	}
	outcomes, err := l.dispatcher.executePass(ctx, shared, nil)
	return append(previous, outcomes...), errors.Join(previousErr, claimErr, err)
}

// Done закрывается после подтверждения текущей пачки, даже если обработка завершилась ошибкой.
func (l *IndependentLane) Done() <-chan struct{} { return l.done }

// Wait дожидается остановки обработчиков после отмены контекста цикла.
func (l *IndependentLane) Wait() {
	if l.done != nil {
		<-l.done
	}
}

func (l *IndependentLane) start(ctx context.Context, tasks []ports.Task) {
	done := make(chan struct{})
	l.done = done
	safego.Run(
		"outbox.independent",
		func() error {
			var err error
			l.outcomes, err = l.dispatcher.executePass(ctx, tasks, l)
			return err
		},
		func(err error) {
			l.err = err
			close(done)
		},
	)
}

func (l *IndependentLane) pauseOnFailure(
	ctx context.Context,
	outcomes []Outcome,
	resolutions []ports.TaskResolution,
) {
	for i, outcome := range outcomes {
		if resolutions[i].NextAt == nil || !l.shouldProbe(outcome.Err) {
			continue
		}
		// Одной пробы и одной отметки в транзакции достаточно на весь проход.
		if l.confirmOutage(ctx) {
			until := l.dispatcher.clock.Now().Add(l.pause)
			resolutions[i].PauseKey = l.gate.Key
			resolutions[i].PauseUntil = &until
		}
		return
	}
}
