// Package kafka — чтение топиков установки на kafka-go: своя группа
// консьюмера, ручной коммит после обработки.
package kafka

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	kafkago "github.com/segmentio/kafka-go"
)

// DefaultRefreshInterval — как часто проверять, не появились ли недостающие топики.
const DefaultRefreshInterval = 30 * time.Second

// errResubscribe — внутренний сигнал: подписка расширилась, читатель пересоздаётся.
var errResubscribe = errors.New("подписка расширилась")

// Config — брокеры, группа и полные имена топиков.
type Config struct {
	Brokers []string
	GroupID string
	Topics  []string
	// RefreshInterval — период проверки недостающих топиков; ноль — DefaultRefreshInterval.
	RefreshInterval time.Duration
	Logger          *slog.Logger
}

// Reader читает группой с ручным коммитом. Новая группа начинает с самого
// раннего сообщения: проекция собирается из истории топиков.
//
// Подписка — только на топики, которые уже есть у брокера. Подписка на
// отсутствующий топик заставила бы брокер с автосозданием завести его с
// настройками по умолчанию: топиками владеет сервер установки, и такой
// топик остался бы с чужим числом партиций и сроком хранения. Недостающие
// топики проверяются по полному списку метаданных, который ничего не
// создаёт; появились — читатель пересоздаётся с расширенной подпиской.
type Reader struct {
	config Config

	mu         sync.Mutex
	group      *kafkago.ConsumerGroup
	runCancel  context.CancelFunc
	subscribed []string
	missing    []string
	checked    time.Time
	closed     bool
}

// NewReader — читатель группы.
func NewReader(cfg Config) *Reader {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.RefreshInterval <= 0 {
		cfg.RefreshInterval = DefaultRefreshInterval
	}

	return &Reader{config: cfg}
}

// Run привязывает обработку к поколению группы. Возврат consume означает,
// что все его воркеры остановлены; только затем допускается новое назначение.
func (r *Reader) Run(ctx context.Context, consume func(context.Context) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	r.mu.Lock()
	if r.closed || r.runCancel != nil {
		r.mu.Unlock()
		return errors.New("читатель закрыт или уже запущен")
	}
	r.runCancel = cancel
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.runCancel = nil
		r.mu.Unlock()
	}()

	for ctx.Err() == nil {
		err := r.nextGeneration(ctx, consume)
		if err != nil && ctx.Err() == nil && !errors.Is(err, errResubscribe) {
			r.config.Logger.Warn("kafka: поколение завершилось с ошибкой", slog.Any("error", err))
			if !sleep(ctx, time.Second) {
				break
			}
		}
	}
	return nil
}

// Close отменяет обработку до выхода из группы, не фиксируя незавершённые сообщения.
func (r *Reader) Close() error {
	r.mu.Lock()
	r.closed = true
	if r.runCancel != nil {
		r.runCancel()
	}
	group := r.group
	r.mu.Unlock()
	if group == nil {
		return nil
	}
	return group.Close()
}

// Subscribed — топики текущей подписки.
func (r *Reader) Subscribed() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.subscribed)
}

// current — группа с актуальной подпиской; пересоздаётся, если появились новые топики.
func (r *Reader) current(ctx context.Context) (*kafkago.ConsumerGroup, error) {
	for {
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()

			return nil, errors.New("читатель закрыт")
		}

		due := r.group == nil || (len(r.missing) > 0 && time.Since(r.checked) >= r.config.RefreshInterval)
		group := r.group
		r.mu.Unlock()

		if !due {
			return group, nil
		}

		group, err := r.refresh(ctx, group)
		if err != nil || group != nil {
			return group, err
		}

		if !sleep(ctx, r.config.RefreshInterval) {
			return nil, ctx.Err()
		}
	}
}

func (r *Reader) refresh(ctx context.Context, previous *kafkago.ConsumerGroup) (*kafkago.ConsumerGroup, error) {
	existing, err := r.existing(ctx)
	if err == nil {
		return r.resubscribe(existing)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if previous == nil {
		return nil, err
	}
	r.config.Logger.Warn("kafka: список топиков недоступен", slog.Any("error", err))
	return previous, nil
}

// resubscribe пересоздаёт группу, если набор существующих топиков вырос.
func (r *Reader) resubscribe(existing []string) (*kafkago.ConsumerGroup, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return nil, errors.New("читатель закрыт")
	}
	r.checked = time.Now()

	subscribe := r.subscription(existing)

	if len(subscribe) == 0 {
		return nil, nil
	}
	if r.group != nil && slices.Equal(subscribe, r.subscribed) {
		return r.group, nil
	}

	if r.group != nil {
		if err := r.group.Close(); err != nil {
			r.config.Logger.Warn("kafka: прежний читатель закрылся с ошибкой", slog.Any("error", err))
		}
	}

	return r.openGroup(subscribe)
}

// Вызывается под mu; снимок недостающих топиков нужен наблюдателю расширения подписки.
func (r *Reader) subscription(existing []string) []string {
	var subscribe, missing []string
	for _, topic := range r.config.Topics {
		if slices.Contains(existing, topic) {
			subscribe = append(subscribe, topic)
		} else {
			missing = append(missing, topic)
		}
	}

	if !slices.Equal(missing, r.missing) && len(missing) > 0 {
		r.config.Logger.Warn(
			"kafka: топики ещё не заведены у брокера, подписка без них",
			slog.Any("missing", missing),
			slog.String("group", r.config.GroupID),
		)
	}
	r.missing = missing

	return subscribe
}

func (r *Reader) openGroup(subscribe []string) (*kafkago.ConsumerGroup, error) {
	r.subscribed = subscribe
	var err error
	r.group, err = kafkago.NewConsumerGroup(kafkago.ConsumerGroupConfig{
		Brokers:               r.config.Brokers,
		ID:                    r.config.GroupID,
		Topics:                subscribe,
		StartOffset:           kafkago.FirstOffset,
		WatchPartitionChanges: true,
		ErrorLogger: kafkago.LoggerFunc(func(format string, args ...any) {
			r.config.Logger.Warn("kafka: "+fmt.Sprintf(format, args...), slog.String("group", r.config.GroupID))
		}),
	})
	if err != nil {
		return nil, err
	}
	r.config.Logger.Info("kafka: подписка", slog.Any("topics", subscribe), slog.String("group", r.config.GroupID))

	return r.group, nil
}

// watch, пока ждётся сообщение, раз в интервал проверяет недостающие топики
// и прерывает ожидание, если какой-то из них появился.
func (r *Reader) watch(ctx context.Context, interrupt func()) func() {
	r.mu.Lock()
	missing := slices.Clone(r.missing)
	r.mu.Unlock()

	if len(missing) == 0 {
		return func() {}
	}

	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		r.watchTopics(ctx, done, missing, interrupt)
	}()
	return func() {
		close(done)
		<-finished
	}
}

func (r *Reader) watchTopics(ctx context.Context, done <-chan struct{}, missing []string, interrupt func()) {
	ticker := time.NewTicker(r.config.RefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			if r.topicsAppeared(ctx, missing) {
				interrupt()
				return
			}
		}
	}
}

func (r *Reader) topicsAppeared(ctx context.Context, missing []string) bool {
	existing, err := r.existing(ctx)
	if err != nil {
		return false
	}
	for _, topic := range missing {
		if slices.Contains(existing, topic) {
			return true
		}
	}
	return false
}

// existing — все топики брокера. Запрос метаданных без списка топиков ничего не создаёт.
func (r *Reader) existing(ctx context.Context) ([]string, error) {
	var lastErr error

	for _, broker := range r.config.Brokers {
		partitions, err := readPartitions(ctx, strings.TrimSpace(broker))
		if err != nil {
			lastErr = err

			continue
		}

		topics := make([]string, 0, len(partitions))
		for _, partition := range partitions {
			if !slices.Contains(topics, partition.Topic) {
				topics = append(topics, partition.Topic)
			}
		}

		return topics, nil
	}

	return nil, fmt.Errorf("список топиков: %w", lastErr)
}

// Сетевое ожидание метаданных тоже заканчивается при остановке или переподписке.
func readPartitions(ctx context.Context, broker string) ([]kafkago.Partition, error) {
	dialer := kafkago.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", broker)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return nil, err
	}
	return conn.ReadPartitions()
}

// Ping проверяет, что хотя бы один брокер принимает соединение.
func Ping(ctx context.Context, brokers []string) error {
	var lastErr error

	for _, broker := range brokers {
		dialer := net.Dialer{Timeout: 2 * time.Second}

		conn, err := dialer.DialContext(ctx, "tcp", strings.TrimSpace(broker))
		if err == nil {
			return conn.Close()
		}

		lastErr = err
	}

	return fmt.Errorf("брокеры недоступны: %w", lastErr)
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
