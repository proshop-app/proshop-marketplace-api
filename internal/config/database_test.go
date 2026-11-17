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
	if err != nil || cfg.Database.Read.Statement != 50*time.Millisecond ||
		cfg.Database.Read.Transaction != 200*time.Millisecond {
		t.Fatal(cfg.Database, err)
	}
	env["DATABASE_READ_STATEMENT_TIMEOUT"] = "1s"
	_, err = config.Load(envOf(env))
	if err == nil || !strings.Contains(err.Error(), "DATABASE_READ_STATEMENT_TIMEOUT") {
		t.Fatal(err)
	}
}
