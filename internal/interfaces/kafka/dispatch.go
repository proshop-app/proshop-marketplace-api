package kafka

import (
	"context"
	"sync"
	"time"

	kafkago "github.com/segmentio/kafka-go"
)

func (c *Consumer) runGeneration(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	var running sync.WaitGroup
	defer cancel()

	incoming := make(chan kafkago.Message)
	jobs := make(chan *delivery)
	completed := make(chan *delivery, c.config.Workers)
	c.startWorkers(ctx, &running, jobs, completed)
	running.Add(1)
	go func() {
		defer running.Done()
		c.fetchMessages(ctx, incoming)
	}()

	commits := &offsetCommitter{progress: make(offsetProgress), commit: c.source.Commit, logger: c.logger}
	c.dispatch(ctx, incoming, jobs, completed, commits)
	cancel()
	running.Wait()
	commits.finish(parent, c.source, completed)
	return nil
}

func (c *Consumer) dispatch(
	ctx context.Context,
	incoming <-chan kafkago.Message,
	jobs chan<- *delivery,
	completed <-chan *delivery,
	commits *offsetCommitter,
) {
	queue := newKeyQueue()
	ticker := time.NewTicker(commitInterval)
	defer ticker.Stop()

	for ctx.Err() == nil {
		output, next := jobs, queue.next()
		if next == nil {
			output = nil
		}
		select {
		case <-ctx.Done():
			return
		case message := <-incoming:
			queue.add(&delivery{
				message: message,
				key:     messageKey(message, c.config.TopicPrefix),
				offset:  commits.progress.add(message),
			})
		case output <- next:
			queue.started()
		case item := <-completed:
			queue.finished(item)
			item.offset.complete()
			c.source.Release(ctx, item.message)
			commits.flush(ctx, false)
		case <-ticker.C:
			commits.flush(ctx, true)
		}
	}
}

func (c *Consumer) fetchMessages(ctx context.Context, incoming chan<- kafkago.Message) {
	for ctx.Err() == nil {
		message, ok := c.next(ctx)
		if !ok {
			return
		}
		select {
		case incoming <- message:
		case <-ctx.Done():
			return
		}
	}
}

func (c *Consumer) startWorkers(
	ctx context.Context,
	running *sync.WaitGroup,
	jobs <-chan *delivery,
	completed chan<- *delivery,
) {
	for range c.config.Workers {
		running.Add(1)
		go func() {
			defer running.Done()
			c.work(ctx, jobs, completed)
		}()
	}
}

func (c *Consumer) work(ctx context.Context, jobs <-chan *delivery, completed chan<- *delivery) {
	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
			return
		case item := <-jobs:
			if ctx.Err() != nil || c.Process(ctx, item.message) != nil {
				return
			}
			select {
			case completed <- item:
			case <-ctx.Done():
				return
			}
		}
	}
}
