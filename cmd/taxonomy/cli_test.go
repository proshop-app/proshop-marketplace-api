package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

func TestCheckDoesNotNeedDatabase(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	var output, diagnostics bytes.Buffer
	if code := run([]string{"check"}, &output, &diagnostics); code != 0 {
		t.Fatalf("code=%d: %s", code, diagnostics.String())
	}
	var info bundleInfo
	if err := json.Unmarshal(output.Bytes(), &info); err != nil || info.Coverage.Leaves == 0 {
		t.Fatalf("нет отчёта о комплекте: %s, %v", output.String(), err)
	}
}

func TestArtifactRelease17Export(t *testing.T) {
	const want = "2b9f84f6a5c071aa42e5751611478ee979a998f4ff42ca09a01799972d848ab2"
	var output, diagnostics bytes.Buffer
	if code := run([]string{"check", "--export"}, &output, &diagnostics); code != 0 {
		t.Fatalf("code=%d: %s", code, diagnostics.String())
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(output.Bytes())); got != want {
		t.Fatalf("SHA-256 развёрнутой выгрузки %s, ожидался %s", got, want)
	}
}

func TestTaxonomyCommandFailsWithoutExposingCredentials(t *testing.T) {
	t.Setenv("DATABASE_URL", "invalid://user:secret-value@host/database")
	for _, args := range [][]string{nil, {"unknown"}, {"apply"}, {"rollback"}, {"check", "extra"}} {
		var output, diagnostics bytes.Buffer
		if code := run(args, &output, &diagnostics); code == 0 {
			t.Fatalf("неверная команда завершилась успешно: %v", args)
		}
		if strings.Contains(diagnostics.String(), "secret-value") {
			t.Fatal("credentials попали в диагностику")
		}
	}
}

func TestRemoveHintRequiresNarrowExplicitCASAndHistoricalProof(t *testing.T) {
	t.Setenv("DATABASE_URL", "invalid://user:secret-value@host/database")
	for _, args := range [][]string{
		{"remove-hint"},
		{"remove-hint", "--attribute", "a1", "--dictionary", "d1", "--previous-version", "4"},
		{
			"remove-hint",
			"--attribute",
			"a1",
			"--dictionary",
			"d1",
			"--previous-version",
			"5",
			"--expected-version",
			"5",
		},
		{"apply", "--attribute", "a1"},
	} {
		var output, diagnostics bytes.Buffer
		code := run(args, &output, &diagnostics)
		if code != 2 || strings.Contains(diagnostics.String(), "secret-value") {
			t.Fatal(args, code, diagnostics.String())
		}
	}
}

func TestApplyPinnedReleaseNeedsNoCategoryReplacementConfirmation(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	var output, diagnostics bytes.Buffer
	if code := run([]string{"apply"}, &output, &diagnostics); code != 2 ||
		!strings.Contains(diagnostics.String(), "DATABASE_URL") {
		t.Fatal(code, diagnostics.String())
	}
}

func TestReleaseFailureReportsDomainReasonWithoutCause(t *testing.T) {
	secret := errors.New("postgres://user:secret-value@private-host/database")
	for _, domain := range []*errs.Error{
		reference.ErrDictionaryNotFound,
		taxonomy.ErrReleaseConflict,
		taxonomy.ErrInvalid.WithMessage("словарь не существует: d1"),
	} {
		t.Run(domain.Key(), func(t *testing.T) {
			var output bytes.Buffer
			err := fmt.Errorf("внешняя ошибка %s: %w", secret.Error(), domain.Wrap(secret))
			code := releaseFailure(&output, err)
			want := "поставка не применена: " + domain.Key() + ": " + domain.Message() + "\n"
			if code != 1 || output.String() != want {
				t.Fatalf("code=%d, диагностика=%q", code, output.String())
			}
		})
	}
	for _, err := range []error{
		secret,
		fmt.Errorf("подключение: %w", secret),
		errs.New(errs.Internal, "storage.internal", secret.Error()).Wrap(secret),
		errs.New(errs.Unavailable, "storage.unavailable", secret.Error()).Wrap(secret),
	} {
		var output bytes.Buffer
		if code := releaseFailure(&output, err); code != 1 ||
			output.String() != "поставка не применена: проверьте миграции, версию и изменения оператора\n" {
			t.Fatalf("code=%d, диагностика=%q", code, output.String())
		}
	}
}
