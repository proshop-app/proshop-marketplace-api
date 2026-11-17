package kafka

import kafkago "github.com/segmentio/kafka-go"

// Очередь принадлежит одному циклу диспетчеризации; воркер получает только delivery.
type keyQueue struct {
	keys  map[orderingKey]*keyMessages
	ready []*keyMessages
}

type keyMessages struct {
	items []*delivery
}

type delivery struct {
	message kafkago.Message
	key     orderingKey
	offset  *offsetSlot
}

func newKeyQueue() *keyQueue {
	return &keyQueue{keys: make(map[orderingKey]*keyMessages)}
}

func (q *keyQueue) add(item *delivery) {
	key := q.keys[item.key]
	if key == nil {
		key = &keyMessages{}
		q.keys[item.key] = key
		q.ready = append(q.ready, key)
	}
	key.items = append(key.items, item)
}

func (q *keyQueue) next() *delivery {
	if len(q.ready) == 0 {
		return nil
	}
	return q.ready[0].items[0]
}

func (q *keyQueue) started() {
	q.ready[0] = nil
	q.ready = q.ready[1:]
}

func (q *keyQueue) finished(item *delivery) {
	key := q.keys[item.key]
	key.items[0] = nil
	key.items = key.items[1:]
	if len(key.items) == 0 {
		delete(q.keys, item.key)
	} else {
		q.ready = append(q.ready, key)
	}
}
