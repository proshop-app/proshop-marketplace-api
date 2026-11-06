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
	Promotion  Promotion
	Moderation Moderation
	ModelKeys  bool
	Identity   Identity
	HTTP       HTTP
	Database   Database
	OpenSearch OpenSearch
	Search     Search
	Kafka      Kafka
	Outbox     Outbox
	Retention  Retention
	Operator   Operator
	Redis      Redis
	SmsRu      SmsRu
	Login      Login
	Platform   Platform
	Cart       Cart
	Checkout   Checkout
	Delivery   Delivery
	Settings   Settings
	Reference  Reference
	LogLevel   string
}

// Redis — сессии покупателей и лимиты частоты.
type Redis struct {
	URL string
}

// Режимы шлюза проверки звонком.
const (
	SmsRuLive    = "live"
	SmsRuSandbox = "sandbox"
)

// SmsRu — шлюз проверки звонком. Песочница сама подтверждает любой номер и
// включается только явно: на бою это вход в чужой аккаунт.
type SmsRu struct {
	Mode                string
	APIID               string
	BaseURL             string
	CallbackURL         string
	SandboxConfirmAfter time.Duration
}

// Limit — не больше Count попыток за Window.
type Limit struct {
	Count  int
	Window time.Duration
}

// Login — сроки и лимиты входа покупателя.
type Login struct {
	ChallengeTTL  time.Duration
	CheckInterval time.Duration
	SessionTTL    time.Duration
	PhoneLimit    Limit
	IPLimit       Limit
	// TrustProxy — адрес покупателя брать из X-Forwarded-For.
	TrustProxy bool
}

// Platform — внутренний API сервера установки.
type Platform struct {
	URL     string
	Token   string
	Timeout time.Duration
}

// Cart — корзины.
type Cart struct {
	TTL time.Duration
}

// Checkout — расчёты доставки.
type Checkout struct {
	DeliverySubsidyMinPercent int
	ReturnBaseURL             string
	OrdersInterval            time.Duration
	QuoteTTL                  time.Duration
	QuoteTimeout              time.Duration
}

// Carrier — учётная запись платформы у перевозчика только на чтение справочника.
type Carrier struct {
	RegionCodes  map[string]string
	URL          string
	ClientID     string
	ClientSecret string
	Token        string
}

// Delivery — справочники пунктов выдачи.
type Delivery struct {
	CDEK            Carrier
	Yandex          Carrier
	RefreshInterval time.Duration
	PickupIndex     string
}

// Settings — сверка настроек магазинов с сервером.
type Settings struct {
	StoreCardRefreshDelay time.Duration
	SyncInterval          time.Duration
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

// Search — общий движок каталога площадки.
type Search struct {
	CandidatesToken     string
	MaintenanceInterval time.Duration
	URL                 string
	Token               string
	Timeout             time.Duration
}

// OpenSearch — кластер геоиндекса пунктов выдачи.
type OpenSearch struct {
	URLs               []string
	Username           string
	Password           string
	InsecureSkipVerify bool
	Shards             int
	Replicas           int
}

// Kafka — брокеры установки и своя группа консьюмера.
type Kafka struct {
	Brokers     []string
	TopicPrefix string
	GroupID     string
	MaxAttempts int
	// ReferenceTopic — имя топика catalog.reference.changed без префикса; топик заводит сервер установки.
	ReferenceTopic         string
	OfferStatusTopic       string
	ParticipantStatusTopic string
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
	Challenges      time.Duration
	Interval        time.Duration
}

// Reference — доступ потребителей к чтению реестра справочных сущностей.
type Reference struct {
	CandidatesInterval time.Duration
	// ReadToken открывает только снимок, resolve и чтение записей: бэкенды
	// поиска и сервера не получают прав оператора площадки. Пусто — читает только оператор.
	ReadToken string
}

// Operator — машинный токен и отдельная учётная запись платформы для панели.
type Operator struct {
	Email        string
	Name         string
	PasswordHash string
	SessionTTL   time.Duration
	Origin       string
	Token        string
}

type reader struct {
	env      func(string) (string, bool)
	problems []string
}

// Load читает полный конфиг сервиса.
func Load(env func(string) (string, bool)) (Config, error) {
	r := reader{env: env}

	cfg := Config{
		Promotion: r.promotion(),
		Identity:  r.identity(),
		HTTP: HTTP{
			Addr:            r.str("HTTP_ADDR", ":8080"),
			ShutdownTimeout: r.duration("SHUTDOWN_TIMEOUT", 20*time.Second),
		},
		Database: r.database(),
		Search: Search{
			CandidatesToken:     r.str("SEARCH_CANDIDATES_TOKEN", ""),
			MaintenanceInterval: r.duration("SEARCH_MAINTENANCE_INTERVAL", time.Minute),
			URL:                 r.httpURL("SEARCH_URL"),
			Token:               r.required("SEARCH_MARKETPLACE_TOKEN"),
			Timeout:             r.duration("SEARCH_TIMEOUT", 35*time.Second),
		},
		OpenSearch: OpenSearch{
			URLs:               r.urls("OPENSEARCH_URLS"),
			Username:           r.str("OPENSEARCH_USERNAME", ""),
			Password:           r.str("OPENSEARCH_PASSWORD", ""),
			InsecureSkipVerify: r.boolean("OPENSEARCH_INSECURE_SKIP_VERIFY", false),
			Shards:             r.positive("OPENSEARCH_SHARDS", 1),
			Replicas:           r.nonNegative("OPENSEARCH_REPLICAS", 1),
		},
		Kafka: Kafka{
			Brokers:                r.list("KAFKA_BROKERS"),
			TopicPrefix:            r.str("KAFKA_TOPIC_PREFIX", ""),
			GroupID:                r.str("KAFKA_GROUP_ID", "marketplace"),
			MaxAttempts:            r.positive("KAFKA_MAX_ATTEMPTS", 5),
			ReferenceTopic:         r.str("KAFKA_TOPIC_REFERENCE_CHANGED", "catalog.reference.changed"),
			OfferStatusTopic:       r.topic("KAFKA_TOPIC_OFFER_STATUS", "marketplace.offer.status"),
			ParticipantStatusTopic: r.topic("KAFKA_TOPIC_PARTICIPANT_STATUS", "marketplace.participant.status"),
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
			Challenges:      r.duration("RETENTION_CHALLENGES", 24*time.Hour),
			Interval:        r.duration("RETENTION_INTERVAL", time.Hour),
		},
		Operator: Operator{
			Token:        r.required("OPERATOR_TOKEN"),
			Email:        r.str("OPERATOR_EMAIL", ""),
			Name:         r.str("OPERATOR_NAME", "Оператор"),
			PasswordHash: r.str("OPERATOR_PASSWORD_HASH", ""),
			SessionTTL:   r.duration("OPERATOR_SESSION_TTL", 8*time.Hour),
			Origin:       r.str("OPERATOR_ORIGIN", ""),
		},
		Redis: Redis{URL: r.redisURL()},
		SmsRu: SmsRu{
			Mode:                r.oneOf("SMSRU_MODE", SmsRuLive, SmsRuLive, SmsRuSandbox),
			APIID:               r.str("SMSRU_API_ID", ""),
			BaseURL:             r.str("SMSRU_BASE_URL", "https://sms.ru"),
			CallbackURL:         r.str("SMSRU_CALLBACK_URL", ""),
			SandboxConfirmAfter: r.duration("SMSRU_SANDBOX_CONFIRM_AFTER", 10*time.Second),
		},
		Login: Login{
			ChallengeTTL:  r.duration("LOGIN_CHALLENGE_TTL", 5*time.Minute),
			CheckInterval: r.duration("LOGIN_CHECK_INTERVAL", 2*time.Second),
			SessionTTL:    r.duration("SESSION_TTL", 30*24*time.Hour),
			PhoneLimit:    r.limit("LOGIN_PHONE_LIMIT", Limit{Count: 3, Window: 10 * time.Minute}),
			IPLimit:       r.limit("LOGIN_IP_LIMIT", Limit{Count: 20, Window: time.Hour}),
			TrustProxy:    r.boolean("HTTP_TRUST_PROXY", false),
		},
		Platform: Platform{
			URL:     r.httpURL("PLATFORM_API_URL"),
			Token:   r.required("SERVICE_TOKEN_MARKETPLACE"),
			Timeout: r.duration("PLATFORM_API_TIMEOUT", 5*time.Second),
		},
		Cart: Cart{TTL: r.duration("CART_TTL", 30*24*time.Hour)},
		Checkout: Checkout{
			DeliverySubsidyMinPercent: r.subsidyPercent(),
			ReturnBaseURL:             r.httpURL("MARKETPLACE_PUBLIC_URL"),
			OrdersInterval:            r.duration("ORDERS_SYNC_INTERVAL", 30*time.Second),
			QuoteTTL:                  r.duration("QUOTE_TTL", 30*time.Minute),
			QuoteTimeout:              r.duration("QUOTE_TIMEOUT", 5*time.Second),
		},
		Delivery: Delivery{
			CDEK: Carrier{
				URL:          r.str("CDEK_API_URL", "https://api.cdek.ru"),
				RegionCodes:  r.regionCodes("CDEK_REGION_CODES"),
				ClientID:     r.str("CDEK_CLIENT_ID", ""),
				ClientSecret: r.str("CDEK_CLIENT_SECRET", ""),
			},
			Yandex: Carrier{
				URL:         r.str("YANDEX_DELIVERY_API_URL", "https://b2b-authproxy.taxi.yandex.net"),
				RegionCodes: r.regionCodes("YANDEX_REGION_CODES"),
				Token:       r.str("YANDEX_DELIVERY_TOKEN", ""),
			},
			RefreshInterval: r.duration("PICKUP_REFRESH_INTERVAL", 24*time.Hour),
			PickupIndex:     r.str("OPENSEARCH_PICKUP_INDEX", "marketplace-pickup-points"),
		},
		Settings: Settings{
			SyncInterval:          r.duration("SETTINGS_SYNC_INTERVAL", 5*time.Minute),
			StoreCardRefreshDelay: r.duration("STORE_CARD_REFRESH_DELAY", 30*time.Second),
		},
		Reference: Reference{
			ReadToken:          r.str("REFERENCE_READ_TOKEN", ""),
			CandidatesInterval: r.optionalInterval("REFERENCE_CANDIDATES_INTERVAL", time.Hour),
		},
		Moderation: Moderation{
			MetricsInterval: r.duration("MODERATION_METRICS_INTERVAL", time.Minute),
			ProbationOffers: r.positive("MODERATION_PROBATION_OFFERS", 50),
			RequireImage:    r.boolean("MODERATION_REQUIRE_IMAGE", true),
		},
		ModelKeys: r.boolean("MARKETPLACE_MODEL_KEYS", false),
		LogLevel:  r.oneOf("LOG_LEVEL", "info", "debug", "info", "warn", "error"),
	}

	if token := cfg.Operator.Token; token != "" && len(token) < minOperatorTokenLength {
		r.fail("OPERATOR_TOKEN", fmt.Sprintf("не короче %d символов", minOperatorTokenLength))
	}
	if len(cfg.Search.Token) < minOperatorTokenLength {
		r.fail("SEARCH_MARKETPLACE_TOKEN", "не короче 32 символов")
	}
	if cfg.Search.Token != "" && (cfg.Search.Token == cfg.Operator.Token || cfg.Search.Token == cfg.Platform.Token) {
		r.fail("SEARCH_MARKETPLACE_TOKEN", "отдельный токен поискового сервиса")
	}

	if (cfg.OpenSearch.Username == "") != (cfg.OpenSearch.Password == "") {
		r.fail("OPENSEARCH_USERNAME", "задаётся вместе с OPENSEARCH_PASSWORD")
	}
	if strings.TrimSpace(cfg.Kafka.GroupID) == "" {
		r.fail("KAFKA_GROUP_ID", "не может быть пустым")
	}
	if strings.TrimSpace(cfg.Kafka.ReferenceTopic) == "" {
		r.fail("KAFKA_TOPIC_REFERENCE_CHANGED", "не может быть пустым")
	}
	if strings.TrimSpace(cfg.Kafka.OfferStatusTopic) == "" {
		r.fail("KAFKA_TOPIC_OFFER_STATUS", "не может быть пустым")
	}
	if strings.TrimSpace(cfg.Kafka.ParticipantStatusTopic) == "" {
		r.fail("KAFKA_TOPIC_PARTICIPANT_STATUS", "не может быть пустым")
	}
	if cfg.Outbox.PerTenant > cfg.Outbox.Batch {
		r.fail("OUTBOX_PER_TENANT", "не больше OUTBOX_BATCH")
	}
	if cfg.SmsRu.Mode == SmsRuLive && cfg.SmsRu.APIID == "" {
		r.fail("SMSRU_API_ID", "обязательна при SMSRU_MODE=live")
	}
	if token := cfg.Platform.Token; token != "" && len(token) < minOperatorTokenLength {
		r.fail("SERVICE_TOKEN_MARKETPLACE", fmt.Sprintf("не короче %d символов", minOperatorTokenLength))
	}
	if (cfg.Delivery.CDEK.ClientID == "") != (cfg.Delivery.CDEK.ClientSecret == "") {
		r.fail("CDEK_CLIENT_ID", "задаётся вместе с CDEK_CLIENT_SECRET")
	}

	r.validatePromotion(cfg)
	r.validateReference(cfg)
	r.validateOperator(cfg.Operator)
	r.validateIdentityOperator(cfg)
	return cfg, r.err()
}

// LoadDatabase читает только базу — для миграций.
func LoadDatabase(env func(string) (string, bool)) (Database, error) {
	r := reader{env: env}
	db := r.database()

	return db, r.err()
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

func (r *reader) redisURL() string {
	raw := r.required("REDIS_URL")
	if raw != "" {
		parsed, err := url.Parse(raw)
		if err != nil || (parsed.Scheme != "redis" && parsed.Scheme != "rediss") {
			r.fail("REDIS_URL", "ожидается адрес redis://…")
		}
	}

	return raw
}

func (r *reader) httpURL(name string) string {
	raw := r.required(name)
	if raw != "" {
		parsed, err := url.Parse(raw)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			r.fail(name, "ожидается адрес http(s)://хост")
		}
	}

	return raw
}

// limit — лимит вида «3/10m»: столько попыток за такое окно.
func (r *reader) limit(name string, fallback Limit) Limit {
	raw, ok := r.lookup(name)
	if !ok {
		return fallback
	}

	count, window, found := strings.Cut(raw, "/")
	value, err := strconv.Atoi(count)
	duration, durationErr := time.ParseDuration(window)

	if !found || err != nil || durationErr != nil || value <= 0 || duration <= 0 {
		r.fail(name, "ожидается «попыток/окно», например 3/10m")

		return fallback
	}

	return Limit{Count: value, Window: duration}
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

func (r *reader) subsidyPercent() int {
	value, ok := r.integer("DELIVERY_SUBSIDY_MIN_PERCENT", 30)
	if ok && (value < 0 || value > 100) {
		r.fail("DELIVERY_SUBSIDY_MIN_PERCENT", "ожидается целый процент от 0 до 100")
	}
	return value
}

// FullTopic — имя топика установки: <prefix>.<имя>, без префикса — само имя.
func (k Kafka) FullTopic(name string) string {
	if k.TopicPrefix == "" {
		return name
	}
	return k.TopicPrefix + "." + name
}

// validateReference: токен читателя не совпадает с секретами, дающими больше прав.
func (r *reader) validateReference(cfg Config) {
	token := cfg.Reference.ReadToken
	if token == "" {
		return
	}
	if len(token) < minOperatorTokenLength {
		r.fail("REFERENCE_READ_TOKEN", fmt.Sprintf("не короче %d символов", minOperatorTokenLength))
	}
	if token == cfg.Operator.Token || token == cfg.Platform.Token || token == cfg.Identity.Secret ||
		token == cfg.Identity.OperatorSecret {
		reason := "отличается от OPERATOR_TOKEN, SERVICE_TOKEN_MARKETPLACE и секретов identity-hub"
		r.fail("REFERENCE_READ_TOKEN", reason)
	}
}

// topic отличает отсутствие настройки от явно заданного пустого имени.
func (r *reader) topic(name, fallback string) string {
	value, ok := r.env(name)
	if !ok {
		return fallback
	}
	return strings.TrimSpace(value)
}
