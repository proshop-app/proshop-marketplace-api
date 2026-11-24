package kafka

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"
)

type finishingSource struct {
	Source
	active bool
}

func (s finishingSource) GenerationActive(context.Context) bool { return s.active }

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

func TestFinalCommitOnlyWhileGenerationLivesAndWithOwnDeadline(t *testing.T) {
	for _, active := range []bool{false, true} {
		progress := make(offsetProgress)
		progress.add(offsetMessage(0, 0)).complete()
		progress.add(offsetMessage(0, 1)) // Незавершённая транзакция не должна быть перекрыта.
		tail := progress.add(offsetMessage(0, 2))
		completed := make(chan []*delivery, 1)
		completed <- []*delivery{{offset: tail}}
		calls := 0
		commits := &offsetCommitter{
			progress: progress,
			logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
			commit: func(ctx context.Context, messages ...kafkago.Message) error {
				calls++
				deadline, bounded := ctx.Deadline()
				if ctx.Err() != nil || !bounded || time.Until(deadline) > finalCommitTimeout {
					t.Fatal("финальный коммит использует отменённый контекст или не имеет своего срока")
				}
				if len(messages) != 1 || messages[0].Offset != 0 {
					t.Fatal("финальный коммит перескочил незавершённое сообщение", messages)
				}
				return nil
			},
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		commits.finish(ctx, finishingSource{active: active}, completed)
		if active && calls != 1 || !active && calls != 0 {
			t.Fatalf("живое поколение %v: финальных коммитов %d", active, calls)
		}
	}
}
