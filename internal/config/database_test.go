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
		pools.Ingestion.MaxConns != 0 {
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
