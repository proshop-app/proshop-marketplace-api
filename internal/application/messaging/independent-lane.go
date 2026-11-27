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

// Finish собирает результат готовой отдельной пачки без нового взятия.
func (l *IndependentLane) Finish() ([]Outcome, error) {
	select {
	case <-l.done:
		outcomes, err := l.outcomes, l.err
		l.done, l.outcomes, l.err = nil, nil, nil
		return outcomes, err
	default:
		return nil, nil
	}
}

// DetachedKinds — виды, срок которых нужно перечитать после завершения отдельной пачки.
func (l *IndependentLane) DetachedKinds() []string { return l.gate.Kinds }

// RunWork берёт указанные виды, пока общая полоса и независимая пачка работают независимо.
func (l *IndependentLane) RunWork(ctx context.Context, kinds []string) ([]Outcome, []string, bool, error) {
	gate := l.gate
	var exclude []string
	if l.done != nil {
		exclude = gate.Kinds
		gate = ports.TaskLaneGate{}
	}
	tasks, full, claimErr := l.dispatcher.claimPass(ctx, gate, exclude, kinds)
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
	return outcomes, taskKinds(tasks), full, errors.Join(claimErr, err)
}

// WorkRequest даёт сроки той же работы, которую сейчас может взять цикл.
func (l *IndependentLane) WorkRequest() ports.ClaimRequest {
	request := l.dispatcher.WorkRequest()
	request.Gate = l.gate
	if l.done != nil {
		request.ExcludeKinds = slices.Concat(request.ExcludeKinds, l.gate.Kinds)
	}
	return request
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
