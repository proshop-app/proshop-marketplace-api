package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/config"
)

const projectionRecoveryTestEvent = `{
	"version":1,
	"tenantId":"t1",
	"productId":"p1",
	"revision":16,
	"operation":"upsert",
	"product":{
		"id":"p1",
		"name":"Товар",
		"price":100,
		"availableQuantity":2,
		"conditions":{"isVisible":true},
		"marketplace":{"categoryId":"c1","attributes":{}}
	}
}`

type projectionRecoveryTestExecutor struct {
	command appmoderation.RecoverProjectionCommand
	calls   int
	err     error
}

func (executor *projectionRecoveryTestExecutor) RecoverProjection(
	_ context.Context,
	command appmoderation.RecoverProjectionCommand,
) (appmoderation.RecoverProjectionResult, error) {
	executor.command = command
	executor.calls++
	return appmoderation.RecoverProjectionResult{
		TenantID:             command.Current.TenantID,
		ProductID:            command.Current.ProductID,
		Operation:            "technical_carry_forward",
		ApprovedRevision:     command.Previous.Revision,
		TargetRevision:       command.Current.Revision,
		BeforeVerdictVersion: command.ExpectedVerdictVersion,
		AfterVerdictVersion:  command.ExpectedVerdictVersion + 1,
		Applied:              command.Apply,
	}, executor.err
}

func projectionRecoveryTestFixture(t *testing.T, previous, current string) ([]string, []byte) {
	t.Helper()
	proof, err := json.Marshal(projectionRecoveryProof{Previous: []byte(previous), Current: []byte(current)})
	if err != nil {
		t.Fatal(err)
	}
	args := []string{
		"--expected-revision=17",
		"--expected-verdict-version=22",
		"--approved-verdict-version=21",
		"--previous-sha256=" + projectionRecoveryTestSHA(previous),
		"--current-sha256=" + projectionRecoveryTestSHA(current),
	}
	return args, proof
}

func projectionRecoveryTestSHA(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func projectionRecoveryTestCurrent() string {
	return strings.Replace(projectionRecoveryTestEvent, `"revision":16`, `"revision":17`, 1)
}

func projectionRecoveryTestConnector(
	executor *projectionRecoveryTestExecutor,
	opened, closed *int,
) projectionRecoveryConnector {
	return func(context.Context, config.Config) (projectionRecoveryExecutor, func(), error) {
		*opened++
		return executor, func() { *closed++ }, nil
	}
}

func assertProjectionRecoveryProofRejected(t *testing.T, args []string, proof []byte) {
	t.Helper()
	var opened, closed int
	var output strings.Builder
	err := runProjectionRecovery(
		context.Background(),
		config.Config{},
		args,
		strings.NewReader(string(proof)),
		&output,
		projectionRecoveryTestConnector(&projectionRecoveryTestExecutor{}, &opened, &closed),
	)
	if !errors.Is(err, errProjectionRecoveryProof) || opened != 0 || closed != 0 || output.Len() != 0 {
		t.Fatalf("проверка=%v open=%d close=%d output=%s", err, opened, closed, &output)
	}
}
