package kafka

import (
	"context"
	"errors"
	"sync"
	"time"

	kafkago "github.com/segmentio/kafka-go"
)

// Окно ограничено на партицию: зависший ключ не останавливает чтение других партиций.
const partitionWindow = 64

// Контекст хранит именно своё поколение: поздний вызов не попадёт в новое назначение.
type generationKey struct{}

type readSession struct {
	generation context.Context
	messages   chan fetchResult
	windows    map[string]map[int]chan struct{}
	commit     func(context.Context, map[string]map[int]int64) error
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
		commit: func(ctx context.Context, offsets map[string]map[int]int64) error {
			return r.commitOffsets(ctx, generation, offsets)
		},
	}
	ctx = context.WithValue(ctx, generationKey{}, session)
	var readers sync.WaitGroup
	for topic, partitions := range generation.Assignments {
		session.windows[topic] = make(map[int]chan struct{})
		for _, partition := range partitions {
			window := make(chan struct{}, partitionWindow)
			session.windows[topic][partition.ID] = window
			readers.Add(1)
			go func() {
				defer readers.Done()
				r.readPartition(ctx, topic, partition, session.messages, window)
			}()
		}
	}
	err := consume(ctx)
	cancel()
	readers.Wait()
	return err
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
) {
	reader := kafkago.NewReader(kafkago.ReaderConfig{
		Brokers:         r.config.Brokers,
		Topic:           topic,
		Partition:       partition.ID,
		MinBytes:        1,
		MaxBytes:        10 << 20,
		MaxWait:         500 * time.Millisecond,
		QueueCapacity:   1,
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
	return nil
}

// GenerationActive отличает остановку обработки от отзыва назначения.
func (r *Reader) GenerationActive(ctx context.Context) bool {
	session, ok := ctx.Value(generationKey{}).(*readSession)
	return ok && session.generation.Err() == nil
}
