package kafka

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/proshop-app/proshop-marketplace/pkg/safego"
)

const (
	// Окно ограничено на партицию: зависший ключ не останавливает чтение других партиций.
	partitionWindow = 64
	// lagInterval — период сверки отставания: один запрос ListOffsets на группу, а не
	// запрос на сообщение; чаще типичного опроса метрик смысла нет.
	lagInterval = 15 * time.Second
	// unknownPosition — у группы ещё нет позиции партиции: она читает с начала журнала,
	// и позицией станет первое прочитанное сообщение.
	unknownPosition = -1
)

// Контекст хранит именно своё поколение: поздний вызов не попадёт в новое назначение.
type generationKey struct{}

type readSession struct {
	generation context.Context
	messages   chan fetchResult
	windows    map[string]map[int]chan struct{}
	// positions — следующая необработанная позиция группы: зафиксированная, а до первой
	// фиксации — начало чтения. Отставание считается от неё, а не от прочитанного:
	// партиция с полным окном и стоящим чтением тоже видна.
	positions map[string]map[int]*atomic.Int64
	commit    func(context.Context, map[string]map[int]int64) error
}

type fetchResult struct {
	message kafkago.Message
	err     error
}

func (r *Reader) nextGeneration(ctx context.Context, consume func(context.Context) error) error {
	group, err := r.current(ctx)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancelCause(ctx)
	stopWatch := r.watch(ctx, func() { cancel(errResubscribe) })
	defer stopWatch()
	defer cancel(nil)

	generation, err := group.Next(ctx)
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	generation.Start(func(generationCtx context.Context) {
		done <- r.consumeGeneration(ctx, generationCtx, generation, consume)
	})
	err = <-done
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	return err
}

func (r *Reader) consumeGeneration(
	parent, generationCtx context.Context,
	generation *kafkago.Generation,
	consume func(context.Context) error,
) error {
	ctx, cancel := generationContext(parent, generationCtx)
	defer cancel()
	session := &readSession{
		generation: generationCtx,
		messages:   make(chan fetchResult),
		windows:    make(map[string]map[int]chan struct{}),
		positions:  make(map[string]map[int]*atomic.Int64),
		commit: func(ctx context.Context, offsets map[string]map[int]int64) error {
			return r.commitOffsets(ctx, generation, offsets)
		},
	}
	ctx = context.WithValue(ctx, generationKey{}, session)
	var readers sync.WaitGroup
	for topic, partitions := range generation.Assignments {
		session.windows[topic] = make(map[int]chan struct{})
		session.positions[topic] = make(map[int]*atomic.Int64)
		for _, partition := range partitions {
			window := make(chan struct{}, partitionWindow)
			position := startPosition(partition.Offset)
			session.windows[topic][partition.ID] = window
			session.positions[topic][partition.ID] = position
			readers.Add(1)
			// Паника чтения партиции останавливает поколение: иначе партиция стояла бы молча.
			safego.Run(
				"kafka.read-partition",
				func() error {
					defer readers.Done()
					r.readPartition(ctx, topic, partition, session.messages, window, position)
					return nil
				},
				func(err error) {
					if err != nil {
						cancel()
					}
				},
			)
		}
	}
	if r.config.Lag != nil {
		readers.Add(1)
		// Паника подсчёта отставания теряет только метрику до следующего поколения.
		safego.Go("kafka.lag", func() {
			defer readers.Done()
			r.reportLag(ctx, session)
		})
	}
	err := consume(ctx)
	cancel()
	readers.Wait()
	return err
}

// startPosition — зафиксированная позиция назначения; служебные отрицательные
// значения (начало или конец журнала) означают, что позиция ещё не известна.
func startPosition(offset int64) *atomic.Int64 {
	position := &atomic.Int64{}
	position.Store(max(offset, unknownPosition))
	return position
}

func generationContext(parent, generation context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(generation, cancel)
	if generation.Err() != nil {
		cancel()
	}
	return ctx, func() {
		stop()
		cancel()
	}
}

func (r *Reader) readPartition(
	ctx context.Context,
	topic string,
	partition kafkago.PartitionAssignment,
	messages chan<- fetchResult,
	window chan struct{},
	position *atomic.Int64,
) {
	reader := kafkago.NewReader(kafkago.ReaderConfig{
		Brokers:       r.config.Brokers,
		Topic:         topic,
		Partition:     partition.ID,
		MinBytes:      1,
		MaxBytes:      10 << 20,
		MaxWait:       500 * time.Millisecond,
		QueueCapacity: 1,
		// Свой опрос отставания kafka-go открывает соединение на партицию; отставание
		// считает reportLag одним запросом на группу.
		ReadLagInterval: -1,
	})
	defer func() { _ = reader.Close() }()
	err := reader.SetOffset(partition.Offset)
	for ctx.Err() == nil {
		select {
		case window <- struct{}{}:
		case <-ctx.Done():
			return
		}
		var message kafkago.Message
		if err == nil {
			message, err = reader.FetchMessage(ctx)
		}
		if err != nil {
			<-window
		} else {
			position.CompareAndSwap(unknownPosition, message.Offset)
		}
		select {
		case messages <- fetchResult{message: message, err: err}:
		case <-ctx.Done():
			return
		}
		if err != nil && !sleep(ctx, time.Second) {
			return
		}
		err = nil
	}
}

// Fetch читает только в контексте callback Run, вместе с его назначением партиций.
func (r *Reader) Fetch(ctx context.Context) (kafkago.Message, error) {
	if err := ctx.Err(); err != nil {
		return kafkago.Message{}, err
	}
	session, ok := ctx.Value(generationKey{}).(*readSession)
	if !ok {
		return kafkago.Message{}, errors.New("чтение вне поколения группы")
	}
	select {
	case <-ctx.Done():
		return kafkago.Message{}, ctx.Err()
	case result := <-session.messages:
		if result.err != nil {
			return result.message, ErrUnavailable.Wrap(result.err)
		}
		return result.message, nil
	}
}

// Release открывает место только в окне исходной партиции и исходного поколения.
func (r *Reader) Release(ctx context.Context, message kafkago.Message) {
	session, ok := ctx.Value(generationKey{}).(*readSession)
	if !ok || ctx.Err() != nil {
		return
	}
	if window := session.windows[message.Topic][message.Partition]; window != nil {
		select {
		case <-window:
		case <-ctx.Done():
		}
	}
}

// Commit фиксирует следующие позиции одной пачкой в исходном поколении группы.
func (r *Reader) Commit(ctx context.Context, messages ...kafkago.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	session, ok := ctx.Value(generationKey{}).(*readSession)
	if !ok {
		return errors.New("коммит вне поколения группы")
	}
	if err := session.generation.Err(); err != nil {
		return err
	}
	offsets := make(map[string]map[int]int64)
	for _, message := range messages {
		if offsets[message.Topic] == nil {
			offsets[message.Topic] = make(map[int]int64)
		}
		offsets[message.Topic][message.Partition] = max(
			offsets[message.Topic][message.Partition],
			message.Offset+1,
		)
	}
	if err := session.commit(ctx, offsets); err != nil {
		return ErrUnavailable.Wrap(err)
	}
	session.advance(offsets)
	return nil
}

// GenerationActive отличает остановку обработки от отзыва назначения.
func (r *Reader) GenerationActive(ctx context.Context) bool {
	session, ok := ctx.Value(generationKey{}).(*readSession)
	return ok && session.generation.Err() == nil
}
