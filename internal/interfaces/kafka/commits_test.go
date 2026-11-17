package kafka

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	kafkago "github.com/segmentio/kafka-go"
)

func TestCommitterBatchesByCountAndTimeAndRetriesWholePrefix(t *testing.T) {
	progress := make(offsetProgress)
	var calls [][]kafkago.Message
	fail := false
	commits := &offsetCommitter{
		progress: progress,
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		commit: func(_ context.Context, messages ...kafkago.Message) error {
			calls = append(calls, messages)
			if fail {
				return errors.New("брокер недоступен")
			}
			return nil
		},
	}
	ctx := context.Background()
	for offset := int64(0); offset < commitBatchSize; offset++ {
		progress.add(offsetMessage(0, offset)).complete()
		commits.flush(ctx, false)
		if offset < commitBatchSize-1 && len(calls) != 0 {
			t.Fatal("коммит на каждое сообщение")
		}
	}
	if len(calls) != 1 || len(calls[0]) != 1 || calls[0][0].Offset != commitBatchSize-1 {
		t.Fatalf("пачка по числу: %v", calls)
	}
	progress.add(offsetMessage(1, 3)).complete()
	commits.flush(ctx, false)
	if len(calls) != 1 {
		t.Fatal("маленькая пачка не ждёт таймера")
	}
	fail = true
	commits.flush(ctx, true)
	if progress.count() != 1 {
		t.Fatal("неудачный коммит потерял префикс")
	}
	progress.add(offsetMessage(0, 100)).complete()
	commits.flush(ctx, false)
	if len(calls) != 2 {
		t.Fatal("ошибка коммита вызывает повторы без паузы")
	}
	fail = false
	commits.flush(ctx, true)
	if len(calls) != 3 || len(calls[2]) != 2 || progress.count() != 0 {
		t.Fatalf("повтор по таймеру не включил обе партиции: %v", calls)
	}
	progress.add(offsetMessage(0, 101)).complete()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	commits.flush(cancelled, true)
	if len(calls) != 3 || progress.count() != 1 {
		t.Fatal("остановка фиксирует смещения")
	}
}
