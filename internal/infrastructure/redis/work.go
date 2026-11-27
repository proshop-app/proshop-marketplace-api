package redis

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/random"
)

const (
	workPingInterval  = time.Minute
	workChannel       = "marketplace:work"
	workPublishWindow = 100 * time.Millisecond
)

// WorkSignals будит локальные циклы и рассылает соседям склеенные подсказки.
type WorkSignals struct {
	client  *goredis.Client
	logger  *slog.Logger
	sender  string
	publish workWatch
	watches []*workWatch
}

type workMessage struct {
	Sender string
	Hints  ports.WorkHints
}

type workWatch struct {
	mu      sync.Mutex
	changed chan struct{}
	hints   ports.WorkHints
	accept  func(string) bool
}

// NewWorkSignals создаёт один подписчик Redis на процесс.
func NewWorkSignals(client *goredis.Client, logger *slog.Logger) *WorkSignals {
	return &WorkSignals{
		client:  client,
		logger:  logger,
		sender:  string(random.IDs{}.NewUUID()),
		publish: workWatch{changed: make(chan struct{}, 1)},
	}
}

// CommandWorkSignal публикует сразу: служебная команда может завершиться после фиксации.
func CommandWorkSignal(url string, logger *slog.Logger) (ports.WorkSignal, func()) {
	client, err := NewClient(url)
	if err != nil {
		return nil, func() {}
	}
	return NewWorkSignals(client, logger), func() { _ = client.Close() }
}

// Watch подключается при сборке циклов, до запуска Run и обработчиков запросов.
func (s *WorkSignals) Watch(accept func(string) bool) ports.WorkWatch {
	watch := &workWatch{changed: make(chan struct{}, 1), hints: make(ports.WorkHints), accept: accept}
	s.watches = append(s.watches, watch)
	return watch
}

func (w *workWatch) Changed() <-chan struct{} { return w.changed }

func (w *workWatch) Take() ports.WorkHints { return w.take(false) }

func (w *workWatch) take(stop bool) ports.WorkHints {
	w.mu.Lock()
	defer w.mu.Unlock()
	hints := w.hints
	w.hints = nil
	if !stop {
		w.hints = make(ports.WorkHints)
	}
	select {
	case <-w.changed:
	default:
	}
	return hints
}

func (w *workWatch) offer(hints ports.WorkHints) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.hints == nil {
		return false
	}
	for kind, next := range hints {
		if kind == "" || w.accept == nil || w.accept(kind) {
			w.hints.Add(kind, next)
		}
	}
	if len(w.hints) > 0 {
		select {
		case w.changed <- struct{}{}:
		default:
		}
	}
	return true
}

// NotifyLocal будит цикл, когда меняется состояние, известное только этому процессу.
func (s *WorkSignals) NotifyLocal(hints ports.WorkHints) {
	for _, watch := range s.watches {
		watch.offer(hints)
	}
}

// Notify будит местные циклы сразу. Без работающего Run публикует прямо из вызова.
func (s *WorkSignals) Notify(hints ports.WorkHints) {
	s.NotifyLocal(hints)
	if !s.publish.offer(hints) {
		s.send(context.Background(), hints)
	}
}

// Run публикует и принимает подсказки. Переподключение запускает сверку.
func (s *WorkSignals) Run(ctx context.Context) error {
	s.publish.Take()
	defer func() { s.send(ctx, s.publish.take(true)) }()
	sub := s.client.Subscribe(ctx, workChannel)
	defer func() { _ = sub.Close() }()
	events := sub.ChannelWithSubscriptions(goredis.WithChannelHealthCheckInterval(workPingInterval))
	wake := s.publish.Changed()
	var timer <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-wake:
			wake = nil
			timer = time.After(workPublishWindow)
		case <-timer:
			timer, wake = nil, s.publish.Changed()
			s.send(ctx, s.publish.Take())
		case event, open := <-events:
			if !open {
				return nil
			}
			switch event := event.(type) {
			case *goredis.Subscription:
				s.NotifyLocal(ports.WorkHints{"": {}})
			case *goredis.Message:
				var message workMessage
				if json.Unmarshal([]byte(event.Payload), &message) == nil && message.Sender != s.sender {
					s.NotifyLocal(message.Hints)
				}
			}
		}
	}
}

func (s *WorkSignals) send(ctx context.Context, hints ports.WorkHints) {
	if len(hints) == 0 {
		return
	}
	body, err := json.Marshal(workMessage{Sender: s.sender, Hints: hints})
	if err != nil {
		s.logger.Error("сигнал очереди: кодирование", slog.Any("error", err))
		return
	}
	attempt, cancel := context.WithTimeout(context.WithoutCancel(ctx), workPublishWindow)
	defer cancel()
	if err := s.client.Publish(attempt, workChannel, body).Err(); err != nil {
		s.logger.Debug("сигнал очереди: Redis недоступен", slog.Any("error", err))
	}
}
