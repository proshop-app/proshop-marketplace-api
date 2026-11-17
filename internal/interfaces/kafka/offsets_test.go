package kafka

import (
	"testing"

	kafkago "github.com/segmentio/kafka-go"
)

func offsetMessage(partition int, offset int64) kafkago.Message {
	return kafkago.Message{Topic: "topic", Partition: partition, Offset: offset}
}

func TestOffsetsOnlyCommitCompletedPartitionPrefix(t *testing.T) {
	progress := make(offsetProgress)
	first := progress.add(offsetMessage(0, 4))
	middle := progress.add(offsetMessage(0, 7))
	last := progress.add(offsetMessage(0, 8))
	other := progress.add(offsetMessage(1, 3))
	middle.complete()
	last.complete()
	other.complete()
	batch, count := progress.batch()
	if count != 1 || len(batch) != 1 || batch[0].Partition != 1 || batch[0].Offset != 3 {
		t.Fatalf("ошибка в начале партиции не задерживает чужую: %v, count=%d", batch, count)
	}
	progress.committed()
	first.complete()
	batch, count = progress.batch()
	if count != 3 || len(batch) != 1 || batch[0].Offset != 8 {
		t.Fatalf("разрыв закрыт, включая отсутствующие в логе числа: %v, count=%d", batch, count)
	}
}

func TestOffsetsErrorInMiddleKeepsEarlierPrefix(t *testing.T) {
	progress := make(offsetProgress)
	first := progress.add(offsetMessage(0, 1))
	middle := progress.add(offsetMessage(0, 2))
	last := progress.add(offsetMessage(0, 3))
	last.complete()
	first.complete()
	batch, count := progress.batch()
	if count != 1 || len(batch) != 1 || batch[0].Offset != 1 {
		t.Fatalf("ошибка не должна быть перекрыта: %v", batch)
	}
	progress.committed()
	middle.complete()
	batch, count = progress.batch()
	if count != 2 || len(batch) != 1 || batch[0].Offset != 3 {
		t.Fatalf("после повтора доступен остаток префикса: %v", batch)
	}
}

func TestOffsetsCompressCompletedRangesBehindBlockedMessage(t *testing.T) {
	progress := make(offsetProgress)
	blocked := progress.add(offsetMessage(0, 0))
	for offset := int64(1); offset <= 10000; offset++ {
		progress.add(offsetMessage(0, offset)).complete()
	}
	if size := blocked.partition.pending.Len(); size != 2 {
		t.Fatalf("память растёт с завершёнными сообщениями: %d", size)
	}
	blocked.complete()
	batch, count := progress.batch()
	if count != 10001 || len(batch) != 1 || batch[0].Offset != 10000 {
		t.Fatalf("диапазон потерян: %v, count=%d", batch, count)
	}
}

func TestOffsetsMergeBothNeighbours(t *testing.T) {
	progress := make(offsetProgress)
	blocked := progress.add(offsetMessage(0, 0))
	left := progress.add(offsetMessage(0, 1))
	middle := progress.add(offsetMessage(0, 2))
	right := progress.add(offsetMessage(0, 3))
	left.complete()
	right.complete()
	middle.complete()
	if size := blocked.partition.pending.Len(); size != 2 {
		t.Fatalf("диапазоны не объединены: %d", size)
	}
	blocked.complete()
	batch, count := progress.batch()
	if count != 4 || batch[0].Offset != 3 {
		t.Fatalf("неверный объединённый диапазон: %v, %d", batch, count)
	}
}

func TestOffsetsKeepPartitionsIndependent(t *testing.T) {
	progress := make(offsetProgress)
	progress.add(offsetMessage(0, 0))
	progress.add(offsetMessage(0, 1)).complete()
	progress.add(offsetMessage(1, 30)).complete()
	batch, count := progress.batch()
	if count != 1 || len(batch) != 1 || batch[0].Partition != 1 || batch[0].Offset != 30 {
		t.Fatalf("ошибка одной партиции задержала другую или пропущена: %v", batch)
	}
}
