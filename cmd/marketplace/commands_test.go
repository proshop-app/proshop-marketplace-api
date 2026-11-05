package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/config"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/referencedata"
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
