package kafka

import (
	"container/list"

	kafkago "github.com/segmentio/kafka-go"
)

type partitionKey struct {
	topic     string
	partition int
}

// offsetProgress отслеживает порядок доставки, а не соседство чисел: в логе бывают пропуски.
type offsetProgress map[partitionKey]*partitionOffsets

type partitionOffsets struct {
	pending list.List
	ready   kafkago.Message
	count   int
}

type offsetSlot struct {
	partition *partitionOffsets
	element   *list.Element
	message   kafkago.Message
	count     int
	done      bool
}

func (p offsetProgress) add(message kafkago.Message) *offsetSlot {
	key := partitionKey{topic: message.Topic, partition: message.Partition}
	partition := p[key]
	if partition == nil {
		partition = &partitionOffsets{}
		p[key] = partition
	}
	// Не удерживаем тела завершённых сообщений за незавершённым префиксом.
	slot := &offsetSlot{
		partition: partition,
		message:   kafkago.Message{Topic: message.Topic, Partition: message.Partition, Offset: message.Offset},
		count:     1,
	}
	slot.element = partition.pending.PushBack(slot)
	return slot
}

func (s *offsetSlot) complete() {
	s.done = true
	pending := &s.partition.pending
	if previous := s.element.Prev(); previous != nil && previous.Value.(*offsetSlot).done {
		left := previous.Value.(*offsetSlot)
		left.message, left.count = s.message, left.count+s.count
		pending.Remove(s.element)
		s = left
	}
	if next := s.element.Next(); next != nil && next.Value.(*offsetSlot).done {
		right := next.Value.(*offsetSlot)
		s.message, s.count = right.message, s.count+right.count
		pending.Remove(next)
	}
	if s.element == pending.Front() {
		s.partition.ready = s.message
		s.partition.count += s.count
		pending.Remove(s.element)
	}
}

func (p offsetProgress) batch() ([]kafkago.Message, int) {
	var messages []kafkago.Message
	count := 0
	for _, partition := range p {
		if partition.count > 0 {
			messages = append(messages, partition.ready)
			count += partition.count
		}
	}
	return messages, count
}

func (p offsetProgress) committed() {
	for _, partition := range p {
		partition.count = 0
	}
}

func (p offsetProgress) count() int {
	count := 0
	for _, partition := range p {
		count += partition.count
	}
	return count
}
