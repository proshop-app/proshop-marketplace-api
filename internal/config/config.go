// Package config читает окружение процесса и проверяет его на старте:
// без обязательной переменной процесс не поднимается, а не падает на первом
// запросе. Все ошибки окружения собираются в одно сообщение.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// minOperatorTokenLength — служебный токен оператора не короче 32 символов.
	minOperatorTokenLength = 32
	// maxConnections — потолок пула соединений с базой.
	maxConnections = 1000
)

// Config — настройки процесса маркетплейса.
type Config struct {
	HTTP       HTTP
	Database   Database
	OpenSearch OpenSearch
	Kafka      Kafka
	Outbox     Outbox
	Retention  Retention
	Operator   Operator
	LogLevel   string
}

// HTTP — сервер REST.
type HTTP struct {
	Addr            string
	ShutdownTimeout time.Duration
}

// Database — PostgreSQL.
type Database struct {
	URL      string
	MaxConns int32
}

// OpenSearch — кластер индекса предложений.
type OpenSearch struct {
	URLs               []string
	Username           string
	Password           string
	InsecureSkipVerify bool
	Index              string
	Shards             int
	Replicas           int
}

// Kafka — брокеры установки и своя группа консьюмера.
type Kafka struct {
	Brokers     []string
	TopicPrefix string
	GroupID     string
	MaxAttempts int
}

// Outbox — разбор очереди задач.
type Outbox struct {
	PollInterval time.Duration
	Batch        int
	PerTenant    int
	Lease        time.Duration
}

// Retention — сроки хранения служебных записей.
type Retention struct {
	ProcessedEvents time.Duration
	OutboxDone      time.Duration
	ParkedEvents    time.Duration
	Interval        time.Duration
}

// Operator — служебный доступ к операторским ручкам до входа оператора через панель.
type Operator struct {
	Token string
}

// Load читает полный конфиг сервиса.
func Load(env func(string) (string, bool)) (Config, error) {
	r := reader{env: env}

	cfg := Config{
		HTTP: HTTP{
			Addr:            r.str("HTTP_ADDR", ":8080"),
			ShutdownTimeout: r.duration("SHUTDOWN_TIMEOUT", 20*time.Second),
		},
		Database: r.database(),
		OpenSearch: OpenSearch{
			URLs:               r.urls("OPENSEARCH_URLS"),
			Username:           r.str("OPENSEARCH_USERNAME", ""),
			Password:           r.str("OPENSEARCH_PASSWORD", ""),
			InsecureSkipVerify: r.boolean("OPENSEARCH_INSECURE_SKIP_VERIFY", false),
			Index:              r.str("OPENSEARCH_INDEX", "marketplace-offers"),
			Shards:             r.positive("OPENSEARCH_SHARDS", 1),
			Replicas:           r.nonNegative("OPENSEARCH_REPLICAS", 1),
		},
		Kafka: Kafka{
			Brokers:     r.list("KAFKA_BROKERS"),
			TopicPrefix: r.str("KAFKA_TOPIC_PREFIX", ""),
			GroupID:     r.str("KAFKA_GROUP_ID", "marketplace"),
			MaxAttempts: r.positive("KAFKA_MAX_ATTEMPTS", 5),
		},
		Outbox: Outbox{
			PollInterval: r.duration("OUTBOX_POLL_INTERVAL", time.Second),
			Batch:        r.positive("OUTBOX_BATCH", 100),
			PerTenant:    r.positive("OUTBOX_PER_TENANT", 20),
			Lease:        r.duration("OUTBOX_LEASE", time.Minute),
		},
		Retention: Retention{
			ProcessedEvents: r.duration("RETENTION_PROCESSED_EVENTS", 14*24*time.Hour),
			OutboxDone:      r.duration("RETENTION_OUTBOX_DONE", 3*24*time.Hour),
			ParkedEvents:    r.duration("RETENTION_PARKED_EVENTS", 30*24*time.Hour),
			Interval:        r.duration("RETENTION_INTERVAL", time.Hour),
		},
		Operator: Operator{Token: r.required("OPERATOR_TOKEN")},
		LogLevel: r.oneOf("LOG_LEVEL", "info", "debug", "info", "warn", "error"),
	}

	if token := cfg.Operator.Token; token != "" && len(token) < minOperatorTokenLength {
		r.fail("OPERATOR_TOKEN", fmt.Sprintf("не короче %d символов", minOperatorTokenLength))
	}
	if (cfg.OpenSearch.Username == "") != (cfg.OpenSearch.Password == "") {
		r.fail("OPENSEARCH_USERNAME", "задаётся вместе с OPENSEARCH_PASSWORD")
	}
	if strings.TrimSpace(cfg.OpenSearch.Index) == "" {
		r.fail("OPENSEARCH_INDEX", "не может быть пустым")
	}
	if strings.TrimSpace(cfg.Kafka.GroupID) == "" {
		r.fail("KAFKA_GROUP_ID", "не может быть пустым")
	}
	if cfg.Outbox.PerTenant > cfg.Outbox.Batch {
		r.fail("OUTBOX_PER_TENANT", "не больше OUTBOX_BATCH")
	}

	return cfg, r.err()
}

// LoadDatabase читает только базу — для миграций.
func LoadDatabase(env func(string) (string, bool)) (Database, error) {
	r := reader{env: env}
	db := r.database()

	return db, r.err()
}

type reader struct {
	env      func(string) (string, bool)
	problems []string
}

func (r *reader) fail(name, reason string) {
	r.problems = append(r.problems, name+": "+reason)
}

func (r *reader) err() error {
	if len(r.problems) == 0 {
		return nil
	}

	return errors.New("некорректное окружение:\n  " + strings.Join(r.problems, "\n  "))
}

func (r *reader) lookup(name string) (string, bool) {
	value, ok := r.env(name)
	value = strings.TrimSpace(value)

	return value, ok && value != ""
}

func (r *reader) str(name, fallback string) string {
	if value, ok := r.lookup(name); ok {
		return value
	}

	return fallback
}

func (r *reader) required(name string) string {
	value, ok := r.lookup(name)
	if !ok {
		r.fail(name, "обязательна")
	}

	return value
}

func (r *reader) database() Database {
	raw := r.required("DATABASE_URL")
	if raw != "" {
		parsed, err := url.Parse(raw)
		if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
			r.fail("DATABASE_URL", "ожидается адрес postgres://…")
		}
	}

	return Database{URL: raw, MaxConns: r.connections("DATABASE_MAX_CONNS", 10)}
}

func (r *reader) list(name string) []string {
	raw := r.required(name)
	if raw == "" {
		return nil
	}

	var items []string
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	if len(items) == 0 {
		r.fail(name, "пустой список")
	}

	return items
}

func (r *reader) urls(name string) []string {
	items := r.list(name)
	for _, item := range items {
		parsed, err := url.Parse(item)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			r.fail(name, "ожидаются адреса http(s)://хост:порт через запятую")

			return items
		}
	}

	return items
}

func (r *reader) integer(name string, fallback int) (int, bool) {
	raw, ok := r.lookup(name)
	if !ok {
		return fallback, true
	}

	value, err := strconv.Atoi(raw)
	if err != nil {
		r.fail(name, "ожидается целое число")

		return fallback, false
	}

	return value, true
}

func (r *reader) positive(name string, fallback int) int {
	value, ok := r.integer(name, fallback)
	if ok && value <= 0 {
		r.fail(name, "ожидается число больше нуля")
	}

	return value
}

// connections — размер пула: от 1 до maxConnections.
func (r *reader) connections(name string, fallback int32) int32 {
	raw, ok := r.lookup(name)
	if !ok {
		return fallback
	}

	value, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || value < 1 || value > maxConnections {
		r.fail(name, fmt.Sprintf("ожидается число от 1 до %d", maxConnections))

		return fallback
	}

	return int32(value)
}

func (r *reader) nonNegative(name string, fallback int) int {
	value, ok := r.integer(name, fallback)
	if ok && value < 0 {
		r.fail(name, "ожидается неотрицательное число")
	}

	return value
}

func (r *reader) duration(name string, fallback time.Duration) time.Duration {
	raw, ok := r.lookup(name)
	if !ok {
		return fallback
	}

	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		r.fail(name, "ожидается положительная длительность (например, 30s, 5m, 24h)")

		return fallback
	}

	return value
}

func (r *reader) boolean(name string, fallback bool) bool {
	raw, ok := r.lookup(name)
	if !ok {
		return fallback
	}

	value, err := strconv.ParseBool(raw)
	if err != nil {
		r.fail(name, "ожидается true или false")

		return fallback
	}

	return value
}

func (r *reader) oneOf(name, fallback string, allowed ...string) string {
	value := r.str(name, fallback)
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}

	r.fail(name, "допустимо: "+strings.Join(allowed, ", "))

	return fallback
}
