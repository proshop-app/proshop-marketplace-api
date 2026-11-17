package kafka

import "testing"

func TestKeyQueueSerializesOneKeyAndSchedulesOthers(t *testing.T) {
	queue := newKeyQueue()
	first := &delivery{key: orderingKey{tenant: "t1", product: "p1"}}
	second := &delivery{key: first.key}
	other := &delivery{key: orderingKey{tenant: "t1", product: "p2"}}
	queue.add(first)
	queue.add(second)
	queue.add(other)
	if queue.next() != first {
		t.Fatal("первое сообщение ключа потеряно")
	}
	queue.started()
	if queue.next() != other {
		t.Fatal("занятый ключ задержал другой")
	}
	queue.started()
	if queue.next() != nil {
		t.Fatal("два сообщения ключа выданы одновременно")
	}
	queue.finished(other)
	queue.finished(first)
	if queue.next() != second {
		t.Fatal("следующее сообщение ключа потеряно")
	}
	queue.started()
	queue.finished(second)
	if len(queue.keys) != 0 || queue.next() != nil {
		t.Fatal("очередь удерживает завершённые ключи")
	}
}
