package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/config"
)

func TestDatabaseBudgetsAreConfigurableAndValidated(t *testing.T) {
	env := validEnv()
	env["DATABASE_READ_STATEMENT_TIMEOUT"] = "50ms"
	env["DATABASE_READ_TRANSACTION_TIMEOUT"] = "200ms"
	cfg, err := config.Load(envOf(env))
	if err != nil || cfg.Database.Read.Budget.Statement != 50*time.Millisecond ||
		cfg.Database.Read.Budget.Transaction != 200*time.Millisecond {
		t.Fatal(cfg.Database, err)
	}
	env["DATABASE_READ_STATEMENT_TIMEOUT"] = "1s"
	_, err = config.Load(envOf(env))
	if err == nil || !strings.Contains(err.Error(), "DATABASE_READ_STATEMENT_TIMEOUT") {
		t.Fatal(err)
	}
}

func TestDatabasePoolsPerLoadClass(t *testing.T) {
	cfg, err := config.Load(envOf(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	pools := cfg.Database
	if pools.Read.MaxConns != 8 || pools.Checkout.MaxConns != 6 || pools.Background.MaxConns != 10 ||
		pools.Write.MaxConns != 6 || pools.Ingestion.MaxConns != 0 {
		t.Fatalf("умолчания пулов; приём без переменной считается по воркерам: %+v", pools)
	}
	env := validEnv()
	env["DATABASE_READ_MAX_CONNS"] = "20"
	env["DATABASE_INGESTION_MAX_CONNS"] = "16"
	cfg, err = config.Load(envOf(env))
	if err != nil || cfg.Database.Read.MaxConns != 20 || cfg.Database.Ingestion.MaxConns != 16 {
		t.Fatal(cfg.Database, err)
	}
	for name, value := range map[string]string{
		"DATABASE_CHECKOUT_MAX_CONNS":   "0",
		"DATABASE_BACKGROUND_MAX_CONNS": "1001",
	} {
		env := validEnv()
		env[name] = value
		if _, err := config.Load(envOf(env)); err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("%s=%s: ожидалась ошибка, получено %v", name, value, err)
		}
	}
}

// pooledEnv — пулы через пулер: адрес пулера без учётных данных, прямой адрес и роль на класс.
func pooledEnv() map[string]string {
	env := validEnv()
	env["DATABASE_URL"] = "postgres://pooler:5432/marketplace"
	env["DATABASE_DIRECT_URL"] = "postgres://marketplace:secret@db:5432/marketplace"
	for _, class := range []string{"READ", "CHECKOUT", "WRITE", "INGESTION", "BACKGROUND", "ORDERS"} {
		env["DATABASE_"+class+"_USER"] = "marketplace_" + strings.ToLower(class)
		env["DATABASE_"+class+"_PASSWORD"] = "secret-" + class
	}
	return env
}

func TestDatabaseWithoutPoolerGoesDirect(t *testing.T) {
	cfg, err := config.Load(envOf(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.Pooled() || cfg.Database.Direct() != cfg.Database.URL {
		t.Fatalf("без DATABASE_DIRECT_URL всё идёт по DATABASE_URL: %+v", cfg.Database)
	}
	env := validEnv()
	env["DATABASE_READ_USER"] = "marketplace_read"
	if _, err := config.Load(envOf(env)); err == nil || !strings.Contains(err.Error(), "DATABASE_READ_USER") {
		t.Fatalf("роль класса без пулера не читается молча: %v", err)
	}
}

func TestDatabaseBehindPoolerNeedsRolePerClass(t *testing.T) {
	cfg, err := config.Load(envOf(pooledEnv()))
	if err != nil {
		t.Fatal(err)
	}
	db := cfg.Database
	if !db.Pooled() || db.Direct() != "postgres://marketplace:secret@db:5432/marketplace" ||
		db.Read.User != "marketplace_read" || db.Orders.Password != "secret-ORDERS" {
		t.Fatalf("адреса и роли классов: %+v", db)
	}
	for name, change := range map[string]func(map[string]string){
		"DATABASE_INGESTION_USER": func(env map[string]string) { delete(env, "DATABASE_INGESTION_PASSWORD") },
		"DATABASE_ORDERS_USER":    func(env map[string]string) { env["DATABASE_ORDERS_USER"] = "marketplace_read" },
		"DATABASE_URL": func(env map[string]string) {
			env["DATABASE_URL"] = "postgres://u:p@pooler/marketplace"
		},
		"DATABASE_DIRECT_URL": func(env map[string]string) {
			env["DATABASE_DIRECT_URL"] = env["DATABASE_URL"]
		},
	} {
		env := pooledEnv()
		change(env)
		if _, err := config.Load(envOf(env)); err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("%s: ожидалась ошибка, получено %v", name, err)
		}
	}
}
