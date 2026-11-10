package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
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
