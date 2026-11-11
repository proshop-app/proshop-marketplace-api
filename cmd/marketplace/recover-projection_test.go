package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/config"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

func TestProjectionRecoveryDefaultsToDryRunAndRequiresExplicitApply(t *testing.T) {
	for _, apply := range []bool{false, true} {
		args, proof := projectionRecoveryTestFixture(t, projectionRecoveryTestEvent, projectionRecoveryTestCurrent())
		if apply {
			args = append(args, "--apply")
		}
		executor := &projectionRecoveryTestExecutor{}
		var output bytes.Buffer
		var opened, closed int
		err := runProjectionRecovery(
			context.Background(),
			config.Config{},
			args,
			bytes.NewReader(proof),
			&output,
			projectionRecoveryTestConnector(executor, &opened, &closed),
		)
		if err != nil || executor.calls != 1 || opened != 1 || closed != 1 {
			t.Fatalf("apply=%v: calls=%d open=%d close=%d err=%v", apply, executor.calls, opened, closed, err)
		}
		command := executor.command
		if command.Apply != apply || command.Current.Revision != 17 || command.Previous.Revision != 16 ||
			command.ExpectedVerdictVersion != 22 || command.ApprovedVerdictVersion != 21 ||
			command.Current.TenantID != "t1" || command.Current.ProductID != "p1" ||
			command.Current.Card.Name != "Товар" || command.Current.Price.Kopecks() != 10000 {
			t.Fatalf("команда не сохранила доказательство: %+v", command)
		}
		var result appmoderation.RecoverProjectionResult
		if err := json.Unmarshal(output.Bytes(), &result); err != nil || result.Applied != apply ||
			result.Operation != "technical_carry_forward" || result.BeforeVerdictVersion != 22 ||
			result.AfterVerdictVersion != 23 || strings.Contains(output.String(), "Товар") {
			t.Fatalf("неверный или избыточный отчёт: %s %v", output.String(), err)
		}
	}
}

func TestProjectionRecoveryRejectsUsageBeforeConnecting(t *testing.T) {
	valid, proof := projectionRecoveryTestFixture(t, projectionRecoveryTestEvent, projectionRecoveryTestCurrent())
	cases := map[string][]string{
		"missing":         nil,
		"unknown":         append(append([]string{}, valid...), "--all"),
		"positional":      append(append([]string{}, valid...), "p2"),
		"after_separator": append(append([]string{}, valid...), "--", "p2"),
		"false_argument":  append(append([]string{}, valid...), "--apply", "false"),
		"bad_apply":       append(append([]string{}, valid...), "--apply=maybe"),
		"zero_revision":   append(append([]string{}, valid...), "--expected-revision=0"),
		"first_revision":  append(append([]string{}, valid...), "--expected-revision=1"),
		"bad_revision":    append(append([]string{}, valid...), "--expected-revision=no"),
		"zero_verdict":    append(append([]string{}, valid...), "--expected-verdict-version=0"),
		"zero_approved":   append(append([]string{}, valid...), "--approved-verdict-version=0"),
		"same_verdict":    append(append([]string{}, valid...), "--approved-verdict-version=22"),
		"missing_sha":     valid[:4],
		"wrong_sha_size":  append(append([]string{}, valid...), "--current-sha256=ab"),
		"wrong_sha_hex":   append(append([]string{}, valid...), "--current-sha256="+strings.Repeat("z", 64)),
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			var opened, closed int
			var output bytes.Buffer
			err := runProjectionRecovery(
				context.Background(),
				config.Config{},
				args,
				bytes.NewReader(proof),
				&output,
				projectionRecoveryTestConnector(&projectionRecoveryTestExecutor{}, &opened, &closed),
			)
			if !errors.Is(err, errProjectionRecoveryUsage) || opened != 0 || closed != 0 || output.Len() != 0 {
				t.Fatalf("неверная команда дошла до БД: %v open=%d close=%d output=%s", err, opened, closed, &output)
			}
		})
	}
}

func TestProjectionRecoveryRejectsUntrustedWireBeforeConnecting(t *testing.T) {
	current := projectionRecoveryTestCurrent()
	cases := map[string]string{
		"different_tenant":      strings.Replace(current, `"t1"`, `"t2"`, 1),
		"different_product":     strings.ReplaceAll(current, `"p1"`, `"p2"`),
		"envelope_product":      strings.Replace(current, `"productId":"p1"`, `"productId":"p2"`, 1),
		"same_revision":         projectionRecoveryTestEvent,
		"earlier_revision":      strings.Replace(current, `"revision":17`, `"revision":15`, 1),
		"unexpected_revision":   strings.Replace(current, `"revision":17`, `"revision":18`, 1),
		"wrong_version":         strings.Replace(current, `"version":1`, `"version":2`, 1),
		"changed_name":          strings.Replace(current, "Товар", "Другой товар", 1),
		"changed_price":         strings.Replace(current, `"price":100`, `"price":200`, 1),
		"changed_unmapped_data": strings.Replace(current, `"name":"Товар"`, `"name":"Товар","extra":true`, 1),
		"unknown_envelope":      strings.Replace(current, `"version":1`, `"version":1,"extra":true`, 1),
		"deleted":               strings.Replace(current, `"operation":"upsert"`, `"operation":"delete"`, 1),
		"trailing_event":        current + `{}`,
		"malformed_event":       `{`,
		"empty_event":           "",
		"oversize_event":        strings.Repeat(" ", maxProjectionRecoveryEvent+1),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			args, proof := projectionRecoveryTestFixture(t, projectionRecoveryTestEvent, raw)
			assertProjectionRecoveryProofRejected(t, args, proof)
		})
	}
	t.Run("skipped_revision_1_to_3", func(t *testing.T) {
		previous := strings.Replace(projectionRecoveryTestEvent, `"revision":16`, `"revision":1`, 1)
		current := strings.Replace(current, `"revision":17`, `"revision":3`, 1)
		args, proof := projectionRecoveryTestFixture(t, previous, current)
		args = append(args, "--expected-revision=3")
		assertProjectionRecoveryProofRejected(t, args, proof)
	})
	for name, change := range map[string]func(string) string{
		"unknown_envelope_both": func(raw string) string {
			return strings.Replace(raw, `"version":1`, `"version":1,"extra":true`, 1)
		},
		"wrong_version_both": func(raw string) string {
			return strings.Replace(raw, `"version":1`, `"version":2`, 1)
		},
		"mismatched_product_both": func(raw string) string {
			return strings.Replace(raw, `"id":"p1"`, `"id":"p2"`, 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			args, proof := projectionRecoveryTestFixture(t, change(projectionRecoveryTestEvent), change(current))
			assertProjectionRecoveryProofRejected(t, args, proof)
		})
	}
}

func TestProjectionRecoveryRejectsMalformedBundleAndSHA(t *testing.T) {
	args, proof := projectionRecoveryTestFixture(t, projectionRecoveryTestEvent, projectionRecoveryTestCurrent())
	cases := map[string][]byte{
		"trailing_json": append(append([]byte{}, proof...), []byte(`{}`)...),
		"unknown_field": append([]byte(`{"extra":true,`), proof[1:]...),
		"bad_base64":    []byte(`{"previous":"!","current":"!"}`),
		"missing_event": []byte(`{}`),
		"null":          []byte(`null`),
		"oversize":      bytes.Repeat([]byte(" "), maxProjectionRecoveryProof+1),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			assertProjectionRecoveryProofRejected(t, args, input)
		})
	}
	for _, name := range []string{"--previous-sha256=", "--current-sha256="} {
		mismatched := append(append([]string{}, args...), name+strings.Repeat("0", 64))
		assertProjectionRecoveryProofRejected(t, mismatched, proof)
	}
}

func TestProjectionRecoveryDoesNotExposeConnectionOrServiceSecrets(t *testing.T) {
	args, proof := projectionRecoveryTestFixture(t, projectionRecoveryTestEvent, projectionRecoveryTestCurrent())
	secretErr := errors.New("postgres://operator:secret-value@database/db")
	for name, serviceErr := range map[string]error{
		"connection": nil,
		"untyped":    secretErr,
		"internal":   errs.New(errs.Internal, "test.internal", "secret-value").Wrap(secretErr),
		"conflict":   errs.New(errs.Conflict, "test.conflict", "версия изменилась").Wrap(secretErr),
	} {
		t.Run(name, func(t *testing.T) {
			executor := &projectionRecoveryTestExecutor{err: serviceErr}
			var opened, closed int
			connect := projectionRecoveryTestConnector(executor, &opened, &closed)
			if name == "connection" {
				connect = func(context.Context, config.Config) (projectionRecoveryExecutor, func(), error) {
					return nil, nil, secretErr
				}
			}
			var output bytes.Buffer
			err := runProjectionRecovery(
				context.Background(),
				config.Config{},
				args,
				bytes.NewReader(proof),
				&output,
				connect,
			)
			if err == nil || strings.Contains(err.Error(), "secret-value") || output.Len() != 0 {
				t.Fatalf("секрет или успех при ошибке: %v %s", err, &output)
			}
			if name != "connection" && (opened != 1 || closed != 1) {
				t.Fatalf("соединение не закрыто: open=%d close=%d", opened, closed)
			}
		})
	}
}

func TestProjectionRecoveryConfigNeedsOnlyDatabaseAndModeration(t *testing.T) {
	env := func(key string) (string, bool) {
		switch key {
		case "DATABASE_URL":
			return "postgres://user:password@database/db", true
		case "MODERATION_REQUIRE_IMAGE":
			return "false", true
		case "MODERATION_PROBATION_OFFERS":
			return "4", true
		case "DATABASE_MAX_CONNS", "MODERATION_METRICS_INTERVAL":
			return "", false
		default:
			t.Fatalf("лишняя переменная: %s", key)
			return "", false
		}
	}
	cfg, err := commandConfig(projectionRecoveryCommand, env)
	if err != nil || cfg.Database.URL == "" || cfg.Moderation.RequireImage || cfg.Moderation.ProbationOffers != 4 {
		t.Fatalf("неверный минимальный граф: %+v %v", cfg, err)
	}
	if _, ok := commands[projectionRecoveryCommand]; !ok {
		t.Fatal("команда не зарегистрирована")
	}
}
