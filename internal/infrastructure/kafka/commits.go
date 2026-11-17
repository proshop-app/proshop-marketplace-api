package kafka

import (
	"context"
	"fmt"

	kafkago "github.com/segmentio/kafka-go"
)

// Generation.CommitOffsets не принимает context. Клиент сохраняет fencing по
// GenerationID/MemberID и соблюдает короткий deadline финального коммита.
func (r *Reader) commitOffsets(
	ctx context.Context,
	generation *kafkago.Generation,
	offsets map[string]map[int]int64,
) error {
	topics := make(map[string][]kafkago.OffsetCommit, len(offsets))
	for topic, partitions := range offsets {
		for partition, offset := range partitions {
			topics[topic] = append(topics[topic], kafkago.OffsetCommit{Partition: partition, Offset: offset})
		}
	}
	response, err := r.client.OffsetCommit(ctx, &kafkago.OffsetCommitRequest{
		GroupID:      generation.GroupID,
		GenerationID: int(generation.ID),
		MemberID:     generation.MemberID,
		Topics:       topics,
	})
	if err != nil {
		return err
	}
	for topic, partitions := range response.Topics {
		for _, partition := range partitions {
			if partition.Error != nil {
				return fmt.Errorf("фиксация %s/%d: %w", topic, partition.Partition, partition.Error)
			}
		}
	}
	return nil
}
