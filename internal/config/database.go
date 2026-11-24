package config

import (
	"net/url"
	"time"
)

// DatabaseBudget задаёт независимые пределы SQL и операции хранения.
type DatabaseBudget struct {
	Statement   time.Duration
	Transaction time.Duration
}

// DatabasePool — пул класса нагрузки: размер зависит от развёртывания, сроки — от класса.
// User и Password — роль класса за пулером: только вместе с DATABASE_DIRECT_URL.
type DatabasePool struct {
	Budget   DatabaseBudget
	MaxConns int32
	User     string
	Password string
}

// Database — PostgreSQL. У каждого класса нагрузки свой пул; Maintenance — сроки
// служебных команд, у которых один небольшой пул.
type Database struct {
	Maintenance DatabaseBudget
	Read        DatabasePool
	Checkout    DatabasePool
	// Ingestion.MaxConns и Orders.MaxConns ноль — по числу воркеров приёма и мест
	// исполнителя оформления; их и сумму пулов проверяет сборка процесса: она знает
	// размеры полос.
	Ingestion  DatabasePool
	Background DatabasePool
	Orders     DatabasePool
	// URL — адрес пулов процесса: база напрямую или пулер в режиме транзакций.
	URL string
	// DirectURL — прямой адрес базы для сеансовых блокировок, миграций и служебных
	// команд. Задан — значит URL ведёт к пулеру; пусто — всё идёт по URL напрямую.
	DirectURL string
}

// databaseClass — имя класса в переменных окружения и его пул.
type databaseClass struct {
	name string
	pool DatabasePool
}

// Pooled — пулы процесса идут через пулер в режиме транзакций.
func (d Database) Pooled() bool {
	return d.DirectURL != ""
}

// Direct — адрес для состояния сеанса: блокировок, миграций и служебных команд.
func (d Database) Direct() string {
	if d.Pooled() {
		return d.DirectURL
	}
	return d.URL
}

func (r *reader) database() Database {
	db := Database{
		URL:         r.postgresURL("DATABASE_URL", r.required("DATABASE_URL")),
		DirectURL:   r.postgresURL("DATABASE_DIRECT_URL", r.str("DATABASE_DIRECT_URL", "")),
		Read:        r.databasePool("READ", 8, 750*time.Millisecond, 2*time.Second),
		Checkout:    r.databasePool("CHECKOUT", 6, 2*time.Second, 5*time.Second),
		Ingestion:   r.databasePool("INGESTION", 0, 2*time.Second, 5*time.Second),
		Background:  r.databasePool("BACKGROUND", 10, 10*time.Second, 30*time.Second),
		Orders:      r.databasePool("ORDERS", 0, 2*time.Second, 5*time.Second),
		Maintenance: r.databaseBudget("MAINTENANCE", 5*time.Minute, 10*time.Minute),
	}
	r.validateRouting(db)
	return db
}

func (r *reader) postgresURL(name, raw string) string {
	if raw == "" {
		return raw
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		r.fail(name, "ожидается адрес postgres://…")
	}
	return raw
}

// validateRouting — через пулер у каждого класса своя роль: пулер держит серверные
// соединения на роль, и только так волна одного класса не занимает соединения другого,
// а сроки класса задаёт роль. Без пулера роли классов не нужны и не читаются молча.
func (r *reader) validateRouting(db Database) {
	classes := []databaseClass{
		{"READ", db.Read},
		{"CHECKOUT", db.Checkout},
		{"INGESTION", db.Ingestion},
		{"BACKGROUND", db.Background},
		{"ORDERS", db.Orders},
	}
	if !db.Pooled() {
		for _, class := range classes {
			if class.pool.User != "" || class.pool.Password != "" {
				r.fail("DATABASE_"+class.name+"_USER", "только вместе с DATABASE_DIRECT_URL — адресом в обход пулера")
			}
		}
		return
	}
	r.validatePooled(db, classes)
}

// validatePooled — у каждого класса своя роль с паролем, адрес пулера без учётных данных.
func (r *reader) validatePooled(db Database, classes []databaseClass) {
	if db.DirectURL == db.URL {
		r.fail("DATABASE_DIRECT_URL", "совпадает с DATABASE_URL: без пулера не задаётся")
	}
	if parsed, err := url.Parse(db.URL); err == nil && parsed.User != nil {
		r.fail("DATABASE_URL", "адрес пулера без учётных данных: у каждого класса своя роль")
	}
	users := map[string]string{}
	for _, class := range classes {
		prefix := "DATABASE_" + class.name
		if class.pool.User == "" || class.pool.Password == "" {
			r.fail(prefix+"_USER", "обязательна вместе с "+prefix+"_PASSWORD при DATABASE_DIRECT_URL")
			continue
		}
		if other, ok := users[class.pool.User]; ok {
			r.fail(prefix+"_USER", "совпадает с ролью класса "+other+": у класса своя роль")
		}
		users[class.pool.User] = class.name
	}
}

// databasePool — пул класса: DATABASE_<КЛАСС>_MAX_CONNS, сроки и роль класса.
func (r *reader) databasePool(class string, size int32, statement, transaction time.Duration) DatabasePool {
	prefix := "DATABASE_" + class
	return DatabasePool{
		MaxConns: r.connections(prefix+"_MAX_CONNS", size),
		Budget:   r.databaseBudget(class, statement, transaction),
		User:     r.str(prefix+"_USER", ""),
		Password: r.str(prefix+"_PASSWORD", ""),
	}
}

func (r *reader) databaseBudget(class string, statement, transaction time.Duration) DatabaseBudget {
	prefix := "DATABASE_" + class
	budget := DatabaseBudget{
		Statement:   r.duration(prefix+"_STATEMENT_TIMEOUT", statement),
		Transaction: r.duration(prefix+"_TRANSACTION_TIMEOUT", transaction),
	}
	if budget.Statement > budget.Transaction {
		r.fail(prefix+"_STATEMENT_TIMEOUT", "не должен превышать срок транзакции")
	}
	return budget
}
