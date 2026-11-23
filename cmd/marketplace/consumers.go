package main

import (
	"context"
	"log/slog"

	"golang.org/x/sync/errgroup"

	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	infrakafka "github.com/proshop-app/proshop-marketplace/internal/infrastructure/kafka"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/kafka"
)

func (s *server) runConsumers(ctx context.Context, group *errgroup.Group) {
	cfg := s.cfg.Kafka
	router := kafka.NewRouter(s.app.projector, s.app.registry).WithOrders(s.app.checkouts)
	regular, bulk := kafka.SubscriptionTopics(cfg.TopicPrefix, router)
	metrics := kafka.NewConsumerMetrics(s.metrics)
	ctx = ports.WithDatabaseClass(ctx, ports.DatabaseIngestion)

	group.Go(func() error {
		return s.consume(ctx, router, metrics, cfg.GroupID, regular, cfg.Workers-cfg.BulkWorkers)
	})
	group.Go(func() error {
		return s.consume(ctx, router, metrics, cfg.GroupID+"-bulk", []string{bulk}, cfg.BulkWorkers)
	})
	group.Go(func() error {
		orders := []string{kafka.OrderStatusTopic(cfg.TopicPrefix)}
		return s.consume(ctx, router, metrics, cfg.GroupID+"-orders", orders, kafka.OrderWorkers)
	})
}

func (s *server) consume(
	ctx context.Context,
	router *kafka.Router,
	metrics *kafka.ConsumerMetrics,
	groupID string,
	topics []string,
	workers int,
) error {
	cfg := s.cfg.Kafka
	reader := infrakafka.NewReader(infrakafka.Config{
		Brokers: cfg.Brokers,
		GroupID: groupID,
		Topics:  topics,
		Logger:  s.logger,
	})
	logger := s.logger.With(slog.String("group", groupID))
	defer func() {
		if err := reader.Close(); err != nil {
			logger.Warn("kafka: читатель закрылся с ошибкой", slog.Any("error", err))
		}
	}()

	consumerConfig := kafka.ConsumerConfig{
		Workers:     workers,
		TopicPrefix: cfg.TopicPrefix,
		MaxAttempts: cfg.MaxAttempts,
		Backoff:     messaging.Backoff,
		Ready:       s.store.pools.Ping,
		Now:         clock.System{}.Now,
	}
	return kafka.NewConsumer(reader, router, s.app.parker, consumerConfig, logger, metrics).Run(ctx)
}
