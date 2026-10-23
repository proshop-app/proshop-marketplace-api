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
	reader     *kafkago.Reader
	subscribed []string
	missing    []string
	checked    time.Time
	closed     bool
}

// NewReader — читатель группы.
func NewReader(cfg Config) *Reader {
	if cfg.RefreshInterval <= 0 {
		cfg.RefreshInterval = DefaultRefreshInterval
	}

	return &Reader{config: cfg}
}

// Fetch — следующее сообщение без коммита. Пока ни одного топика нет, ждёт их появления.
func (r *Reader) Fetch(ctx context.Context) (kafkago.Message, error) {
	for {
		reader, err := r.current(ctx)
		if err != nil {
			return kafkago.Message{}, err
		}

		fetchCtx, cancel := context.WithCancelCause(ctx)
		stopWatch := r.watch(fetchCtx, func() { cancel(errResubscribe) })
		message, err := reader.FetchMessage(fetchCtx)
		stopWatch()
		cancel(nil)

		if err != nil && errors.Is(context.Cause(fetchCtx), errResubscribe) && ctx.Err() == nil {
			continue
		}

		return message, err
	}
}

// Commit фиксирует позицию после сообщения. Сообщение, прочитанное до
// переподписки, не фиксируется: оно придёт снова и отсеется дедупом.
func (r *Reader) Commit(ctx context.Context, message kafkago.Message) error {
	r.mu.Lock()
	reader := r.reader
	r.mu.Unlock()

	if reader == nil {
		return errors.New("читатель не запущен")
	}

	return reader.CommitMessages(ctx, message)
}

// Close закрывает читатель и выходит из группы.
func (r *Reader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.closed = true
	if r.reader == nil {
		return nil
	}

	return r.reader.Close()
}

// Subscribed — топики текущей подписки.
func (r *Reader) Subscribed() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.subscribed)
}

// current — читатель с актуальной подпиской; пересоздаётся, если появились новые топики.
func (r *Reader) current(ctx context.Context) (*kafkago.Reader, error) {
	for {
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()

			return nil, errors.New("читатель закрыт")
		}

		due := r.reader == nil || (len(r.missing) > 0 && time.Since(r.checked) >= r.config.RefreshInterval)
		reader := r.reader
		r.mu.Unlock()

		if !due {
			return reader, nil
		}

		existing, err := r.existing(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if reader != nil {
				r.config.Logger.Warn("kafka: список топиков недоступен", slog.Any("error", err))

				return reader, nil
			}

			return nil, err
		}

		if reader, ok := r.resubscribe(existing); ok {
			return reader, nil
		}

		if !sleep(ctx, r.config.RefreshInterval) {
			return nil, ctx.Err()
		}
	}
}

// resubscribe пересоздаёт читатель, если набор существующих топиков вырос.
func (r *Reader) resubscribe(existing []string) (*kafkago.Reader, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.checked = time.Now()

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

	if len(subscribe) == 0 {
		return nil, false
	}
	if r.reader != nil && slices.Equal(subscribe, r.subscribed) {
		return r.reader, true
	}

	if r.reader != nil {
		if err := r.reader.Close(); err != nil {
			r.config.Logger.Warn("kafka: прежний читатель закрылся с ошибкой", slog.Any("error", err))
		}
	}

	r.subscribed = subscribe
	r.reader = kafkago.NewReader(kafkago.ReaderConfig{
		Brokers:               r.config.Brokers,
		GroupID:               r.config.GroupID,
		GroupTopics:           subscribe,
		MinBytes:              1,
		MaxBytes:              10 << 20,
		MaxWait:               500 * time.Millisecond,
		StartOffset:           kafkago.FirstOffset,
		CommitInterval:        0,
		WatchPartitionChanges: true,
		ErrorLogger: kafkago.LoggerFunc(func(format string, args ...any) {
			r.config.Logger.Warn("kafka: "+fmt.Sprintf(format, args...), slog.String("group", r.config.GroupID))
		}),
	})
	r.config.Logger.Info("kafka: подписка", slog.Any("topics", subscribe), slog.String("group", r.config.GroupID))

	return r.reader, true
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

	go func() {
		ticker := time.NewTicker(r.config.RefreshInterval)
		defer ticker.Stop()

		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				existing, err := r.existing(ctx)
				if err != nil {
					continue
				}

				for _, topic := range missing {
					if slices.Contains(existing, topic) {
						interrupt()

						return
					}
				}
			}
		}
	}()

	return func() { close(done) }
}

// existing — все топики брокера. Запрос метаданных без списка топиков ничего не создаёт.
func (r *Reader) existing(ctx context.Context) ([]string, error) {
	var lastErr error

	for _, broker := range r.config.Brokers {
		dialer := kafkago.Dialer{Timeout: 5 * time.Second}

		conn, err := dialer.DialContext(ctx, "tcp", strings.TrimSpace(broker))
		if err != nil {
			lastErr = err

			continue
		}

		partitions, err := conn.ReadPartitions()
		_ = conn.Close()

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
