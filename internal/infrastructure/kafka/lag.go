package kafka

import (
	"context"
	"log/slog"
	"time"

	kafkago "github.com/segmentio/kafka-go"
)

// LagObserver получает отставание группы по партиции: сколько сообщений журнала
// группа ещё не обработала. Forget — партиция ушла из назначения этого процесса.
type LagObserver interface {
	ObserveLag(topic string, partition int, lag int64)
	ForgetLag(topic string, partition int)
}

// reportLag раз в lagInterval одним запросом ListOffsets сверяет конец журнала
// назначенных партиций с позицией группы. Так видны и волна приёма, и застрявшая
// партиция, у которой окно полно и чтение стоит; брокер не спрашивается на каждое
// сообщение, как ReadLagInterval читателя kafka-go (соединение на партицию).
func (r *Reader) reportLag(ctx context.Context, session *readSession) {
	defer session.forgetLag(r.config.Lag)
	ticker := time.NewTicker(lagInterval)
	defer ticker.Stop()
	for {
		r.observeLag(ctx, session)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *Reader) observeLag(ctx context.Context, session *readSession) {
	request := &kafkago.ListOffsetsRequest{Topics: make(map[string][]kafkago.OffsetRequest, len(session.positions))}
	for topic, partitions := range session.positions {
		for partition := range partitions {
			request.Topics[topic] = append(request.Topics[topic], kafkago.LastOffsetOf(partition))
		}
	}
	response, err := r.client.ListOffsets(ctx, request)
	if err != nil {
		if ctx.Err() == nil {
			r.config.Logger.Warn("kafka: отставание группы не прочитано", slog.Any("error", err))
		}
		return
	}
	for topic, partitions := range response.Topics {
		for _, offsets := range partitions {
			position := session.positions[topic][offsets.Partition]
			if offsets.Error != nil || offsets.LastOffset < 0 || position == nil || position.Load() == unknownPosition {
				continue
			}
			r.config.Lag.ObserveLag(topic, offsets.Partition, max(0, offsets.LastOffset-position.Load()))
		}
	}
}

// advance переносит позицию группы на зафиксированные смещения; позиция только растёт.
func (s *readSession) advance(offsets map[string]map[int]int64) {
	for topic, partitions := range offsets {
		for partition, offset := range partitions {
			position := s.positions[topic][partition]
			if position == nil {
				continue
			}
			for current := position.Load(); offset > current; current = position.Load() {
				if position.CompareAndSwap(current, offset) {
					break
				}
			}
		}
	}
}

func (s *readSession) forgetLag(observer LagObserver) {
	for topic, partitions := range s.positions {
		for partition := range partitions {
			observer.ForgetLag(topic, partition)
		}
	}
}
