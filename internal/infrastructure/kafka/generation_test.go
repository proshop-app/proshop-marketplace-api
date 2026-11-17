package kafka

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"
)

func TestGenerationContextCancelsOnRevokeAndStop(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		parent, stop := context.WithCancel(context.Background())
		generation, rebalance := context.WithCancel(context.Background())
		ctx, cancel := generationContext(parent, generation)
		if revoke {
			rebalance()
		} else {
			stop()
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
			t.Fatal("обработка пережила своё назначение")
		}
		cancel()
		stop()
		rebalance()
	}
}

func TestCommitUsesOriginalGenerationAndNextOffsets(t *testing.T) {
	generation, revoke := context.WithCancel(context.Background())
	defer revoke()
	var got map[string]map[int]int64
	calls := 0
	session := &readSession{
		generation: generation,
		commit: func(_ context.Context, offsets map[string]map[int]int64) error {
			calls++
			got = offsets
			return nil
		},
	}
	ctx := context.WithValue(context.Background(), generationKey{}, session)
	reader := NewReader(Config{})
	err := reader.Commit(
		ctx,
		kafkago.Message{Topic: "a", Partition: 0, Offset: 7},
		kafkago.Message{Topic: "a", Partition: 0, Offset: 5},
		kafkago.Message{Topic: "a", Partition: 1, Offset: 9},
		kafkago.Message{Topic: "b", Partition: 0, Offset: 2},
	)
	want := map[string]map[int]int64{"a": {0: 8, 1: 10}, "b": {0: 3}}
	if err != nil || calls != 1 || !reflect.DeepEqual(got, want) {
		t.Fatalf("пачка смещений: %+v, вызовов %d, ошибка %v", got, calls, err)
	}
	revoke()
	// Даже до доставки отмены дочернему контексту поколение уже ограждено.
	if err := reader.Commit(ctx, kafkago.Message{Topic: "a", Offset: 20}); !errors.Is(err, context.Canceled) {
		t.Fatalf("отозванное поколение подтвердило позицию: %v", err)
	}
	if calls != 1 {
		t.Fatal("поздний коммит попал в другое назначение")
	}
}

func TestSessionRejectsUnboundAndCancelledOperations(t *testing.T) {
	reader := NewReader(Config{})
	if _, err := reader.Fetch(context.Background()); err == nil {
		t.Fatal("чтение вне callback поколения")
	}
	if err := reader.Commit(context.Background(), kafkago.Message{}); err == nil {
		t.Fatal("коммит вне callback поколения")
	}
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	session := &readSession{generation: parent, messages: make(chan fetchResult)}
	ctx := context.WithValue(parent, generationKey{}, session)
	if _, err := reader.Fetch(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("отозванное чтение: %v", err)
	}
	if err := reader.Commit(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("отозванный коммит: %v", err)
	}
}

func TestReleaseKeepsPartitionWindowsAndGenerationsIndependent(t *testing.T) {
	reader := NewReader(Config{})
	blocked := make(chan struct{}, partitionWindow)
	other := make(chan struct{}, partitionWindow)
	for range partitionWindow {
		blocked <- struct{}{}
		other <- struct{}{}
	}
	session := &readSession{windows: map[string]map[int]chan struct{}{"topic": {0: blocked, 1: other}}}
	ctx := context.WithValue(context.Background(), generationKey{}, session)
	reader.Release(ctx, kafkago.Message{Topic: "topic", Partition: 1})
	if len(blocked) != partitionWindow || len(other) != partitionWindow-1 {
		t.Fatal("обработка другой партиции изменила окно зависшей")
	}
	resumed := make(chan struct{})
	go func() {
		blocked <- struct{}{}
		close(resumed)
	}()
	select {
	case <-resumed:
		t.Fatal("полное окно продолжает читать зависшую партицию")
	default:
	}
	reader.Release(ctx, kafkago.Message{Topic: "topic", Partition: 0})
	select {
	case <-resumed:
	case <-time.After(time.Second):
		t.Fatal("завершение сообщения не возобновило партицию")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	reader.Release(cancelled, kafkago.Message{Topic: "topic", Partition: 0})
	if len(blocked) != partitionWindow {
		t.Fatal("позднее завершение вмешалось в окно после отмены")
	}
}

func TestGenerationActiveSurvivesProcessCancellationOnly(t *testing.T) {
	generation, revoke := context.WithCancel(context.Background())
	defer revoke()
	parent, stop := context.WithCancel(context.Background())
	ctx := context.WithValue(parent, generationKey{}, &readSession{generation: generation})
	reader := NewReader(Config{})
	stop()
	if !reader.GenerationActive(ctx) {
		t.Fatal("остановка процесса выдана за отзыв партиций")
	}
	revoke()
	if reader.GenerationActive(context.WithoutCancel(ctx)) {
		t.Fatal("отмена родителя скрыла отзыв назначения")
	}
}
