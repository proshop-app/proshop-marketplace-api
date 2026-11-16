package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/config"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

func TestReferenceConfigNeedsOnlyDatabase(t *testing.T) {
	env := func(key string) (string, bool) {
		if key == "DATABASE_MAX_CONNS" {
			return "", false
		}
		if key != "DATABASE_URL" {
			t.Fatalf("лишняя переменная: %s", key)
		}
		return "postgres://user:password@database/db", true
	}
	cfg, err := commandConfig("reference", env)
	if err != nil || cfg.Database.URL == "" {
		t.Fatalf("конфигурация поставки: %v", err)
	}
	_, err = commandConfig("reference", func(string) (string, bool) { return "", false })
	if err == nil {
		t.Fatal("принята конфигурация без базы")
	}
}

func TestReferenceImportRejectsUsage(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"apply"},
		{"apply-all"},
		{"rollback"},
		{"check"},
		{"import"},
		{"import", "--file"},
		{"import", "--file", ""},
		{"import", "--dir", "directory"},
		{"import", "--file", "package.json", "extra"},
	} {
		err := runReferenceCommand(context.Background(), config.Config{}, args, &bytes.Buffer{})
		if !errors.Is(err, errReferenceUsage) {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

func TestReferenceImportValidatesBeforeDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "package.json")
	if err := os.WriteFile(path, []byte(`{"schemaVersion": 2}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{path, path + ".missing"} {
		var output bytes.Buffer
		args := []string{"import", "--file", file}
		err := runReferenceCommand(context.Background(), config.Config{}, args, &output)
		if err == nil || !strings.Contains(err.Error(), reference.ErrInvalidDelivery.Key()) || output.Len() != 0 {
			t.Fatalf("ошибка файла скрыта подключением к базе: %v, %s", err, output.String())
		}
	}
}

func TestDeliveryFailureHidesDriverAndWrapper(t *testing.T) {
	secret := errors.New("postgres://user:secret-value@private-host/database")
	domain := reference.ErrDeliveryConflict.WithMessage("несовместимая поставка")
	wrapped := fmt.Errorf("%s: %w", secret.Error(), domain.Wrap(secret))
	want := "пакет справочников не импортирован: " + domain.Key() + ": " + domain.Message()
	if got := referenceImportFailure(wrapped).Error(); got != want {
		t.Fatal(got)
	}
	for _, err := range []error{
		secret,
		errs.New(errs.Internal, "storage.internal", secret.Error()).Wrap(secret),
		errs.New(errs.Unavailable, "storage.unavailable", secret.Error()).Wrap(secret),
		fmt.Errorf("%s: %w", secret.Error(), context.Canceled),
		fmt.Errorf("%s: %w", secret.Error(), context.DeadlineExceeded),
	} {
		if got := referenceImportFailure(err).Error(); strings.Contains(got, "secret-value") ||
			strings.Contains(got, "private-host") {
			t.Fatal("реквизиты попали в диагностику", got)
		}
	}
}
