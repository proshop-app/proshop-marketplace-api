package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/config"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/referencedata"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

const referenceBundle = "../../internal/infrastructure/referencedata/testdata/b1"

func TestReferenceCheckDoesNotNeedDatabase(t *testing.T) {
	var output bytes.Buffer
	args := []string{"check", "--dir", referenceBundle}
	err := runReferenceDelivery(context.Background(), config.Config{}, args, &output)
	if err != nil {
		t.Fatal(err)
	}
	var summary referencedata.Summary
	if err := json.Unmarshal(output.Bytes(), &summary); err != nil || summary.Entries != 2 || summary.Release != 1 {
		t.Fatalf("отчёт check: %s %v", output.String(), err)
	}
}

func TestReferenceCommandRejectsUsageAndHidesCredentials(t *testing.T) {
	usages := [][]string{nil, {"unknown"}, {"apply"}, {"check", "--dir"}, {"check", "--dir", referenceBundle, "x"}}
	for _, args := range usages {
		err := runReferenceDelivery(context.Background(), config.Config{}, args, &bytes.Buffer{})
		if !errors.Is(err, errReferenceUsage) {
			t.Fatalf("%v: %v", args, err)
		}
	}
	url := "postgres://user:secret-value@127.0.0.1:1/db?connect_timeout=1"
	cfg := config.Config{Database: config.Database{URL: url}}
	err := runReferenceDelivery(context.Background(), cfg, []string{"apply", "--dir", referenceBundle}, &bytes.Buffer{})
	if err == nil || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("ошибка базы: %v", err)
	}
}

func TestReferenceDirectory(t *testing.T) {
	for _, action := range []string{"apply-all", "apply", "check", "rollback"} {
		dir, err := referenceDirectory([]string{action, "--dir", "/explicit"})
		if err != nil || dir != "/explicit" {
			t.Fatalf("%s: %q, %v", action, dir, err)
		}
	}
	dir, err := referenceDirectory([]string{"apply-all"})
	if err != nil || dir != "/reference" {
		t.Fatalf("каталог по умолчанию: %q, %v", dir, err)
	}
	if _, err := referenceDirectory([]string{"apply-all", "--dir", ""}); err == nil {
		t.Fatal("пустой явный каталог принят")
	}
}

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
		{"import"},
		{"import", "--file"},
		{"import", "--file", ""},
		{"import", "--dir", referenceBundle},
		{"import", "--file", "package.json", "extra"},
	} {
		err := runReferenceDelivery(context.Background(), config.Config{}, args, &bytes.Buffer{})
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
		err := runReferenceDelivery(context.Background(), config.Config{}, args, &output)
		if err == nil || !strings.Contains(err.Error(), reference.ErrInvalidDelivery.Key()) || output.Len() != 0 {
			t.Fatalf("ошибка файла скрыта подключением к базе: %v, %s", err, output.String())
		}
	}
}

func TestDeliveryFailureHidesDriverAndWrapper(t *testing.T) {
	secret := errors.New("postgres://user:secret-value@private-host/database")
	domain := reference.ErrDeliveryConflict.WithMessage("несовместимая поставка")
	wrapped := fmt.Errorf("%s: %w", secret.Error(), domain.Wrap(secret))
	want := "поставка реестра не применена: " + domain.Key() + ": " + domain.Message()
	if got := deliveryFailure(wrapped).Error(); got != want {
		t.Fatal(got)
	}
	for _, err := range []error{
		secret,
		errs.New(errs.Internal, "storage.internal", secret.Error()).Wrap(secret),
		errs.New(errs.Unavailable, "storage.unavailable", secret.Error()).Wrap(secret),
		fmt.Errorf("%s: %w", secret.Error(), context.Canceled),
		fmt.Errorf("%s: %w", secret.Error(), context.DeadlineExceeded),
	} {
		if got := deliveryFailure(err).Error(); strings.Contains(got, "secret-value") ||
			strings.Contains(got, "private-host") {
			t.Fatal("реквизиты попали в диагностику", got)
		}
	}
}
