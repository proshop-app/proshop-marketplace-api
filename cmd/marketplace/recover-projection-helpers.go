package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"reflect"
	"strings"
	"time"

	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/kafka"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

const (
	maxProjectionRecoveryProof = 4 << 20
	maxProjectionRecoveryEvent = 1 << 20
)

type projectionRecoveryOptions struct {
	expectedRevision       int64
	expectedVerdictVersion int64
	approvedVerdictVersion int64
	previousSHA            string
	currentSHA             string
	apply                  bool
}

type projectionRecoveryProof struct {
	Previous []byte `json:"previous"`
	Current  []byte `json:"current"`
}

func projectionRecoveryOptionsFrom(args []string) (projectionRecoveryOptions, error) {
	var options projectionRecoveryOptions
	flags := flag.NewFlagSet(projectionRecoveryCommand, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Int64Var(&options.expectedRevision, "expected-revision", 0, "текущая ревизия товара")
	flags.Int64Var(&options.expectedVerdictVersion, "expected-verdict-version", 0, "текущая версия вердикта")
	flags.Int64Var(&options.approvedVerdictVersion, "approved-verdict-version", 0, "проверенная версия допуска")
	flags.StringVar(&options.previousSHA, "previous-sha256", "", "SHA прежних retained wire bytes")
	flags.StringVar(&options.currentSHA, "current-sha256", "", "SHA текущих retained wire bytes")
	flags.BoolVar(&options.apply, "apply", false, "применить технический CAS после проверки")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return options, errProjectionRecoveryUsage
	}
	if options.expectedRevision < 2 || options.expectedVerdictVersion < 1 || options.approvedVerdictVersion < 1 ||
		options.approvedVerdictVersion >= options.expectedVerdictVersion ||
		!validProjectionRecoverySHA(options.previousSHA) || !validProjectionRecoverySHA(options.currentSHA) {
		return options, errProjectionRecoveryUsage
	}

	return options, nil
}

func validProjectionRecoverySHA(value string) bool {
	decoded, err := hex.DecodeString(value)

	return err == nil && len(decoded) == sha256.Size
}

func readProjectionRecoveryProof(
	input io.Reader,
	options projectionRecoveryOptions,
	at time.Time,
) (appmoderation.RecoverProjectionCommand, error) {
	var command appmoderation.RecoverProjectionCommand
	limited := io.LimitReader(input, maxProjectionRecoveryProof+1)
	raw, err := io.ReadAll(limited)
	if err != nil || len(raw) > maxProjectionRecoveryProof {
		return command, errProjectionRecoveryProof
	}
	var proof projectionRecoveryProof
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&proof); err != nil {
		return command, errProjectionRecoveryProof
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return command, errProjectionRecoveryProof
	}
	if !matchingProjectionRecoverySHA(proof.Previous, options.previousSHA) ||
		!matchingProjectionRecoverySHA(proof.Current, options.currentSHA) {
		return command, errProjectionRecoveryProof
	}
	previousBody, err := projectionRecoveryEventBody(proof.Previous)
	if err != nil {
		return command, err
	}
	currentBody, err := projectionRecoveryEventBody(proof.Current)
	if err != nil || !reflect.DeepEqual(previousBody, currentBody) {
		return command, errProjectionRecoveryProof
	}
	previous, err := kafka.DecodeProjectionSnapshot(proof.Previous, at)
	if err != nil {
		return command, errProjectionRecoveryProof
	}
	current, err := kafka.DecodeProjectionSnapshot(proof.Current, at)
	if err != nil || previous.Deleted || current.Deleted || previous.Revision < 1 ||
		current.Revision-previous.Revision != 1 ||
		current.Revision != options.expectedRevision || previous.TenantID != current.TenantID ||
		previous.ProductID != current.ProductID {
		return command, errProjectionRecoveryProof
	}

	return appmoderation.RecoverProjectionCommand{
		Previous:               previous,
		Current:                current,
		ExpectedRevision:       options.expectedRevision,
		ExpectedVerdictVersion: options.expectedVerdictVersion,
		ApprovedVerdictVersion: options.approvedVerdictVersion,
		Apply:                  options.apply,
	}, nil
}

func matchingProjectionRecoverySHA(raw []byte, expected string) bool {
	if len(raw) == 0 || len(raw) > maxProjectionRecoveryEvent {
		return false
	}
	actual := sha256.Sum256(raw)

	return hex.EncodeToString(actual[:]) == strings.ToLower(expected)
}

// В этом maintenance-пути сравнивается весь исходный event, включая поля,
// которые основной Kafka mapper не использует. Отличаться может только revision.
func projectionRecoveryEventBody(raw []byte) (map[string]any, error) {
	if !json.Valid(raw) {
		return nil, errProjectionRecoveryProof
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var event map[string]any
	if err := decoder.Decode(&event); err != nil || len(event) != 6 {
		return nil, errProjectionRecoveryProof
	}
	for key := range event {
		if key != "version" && key != "tenantId" && key != "productId" && key != "revision" &&
			key != "operation" && key != "product" {
			return nil, errProjectionRecoveryProof
		}
	}
	if event["operation"] != "upsert" || event["product"] == nil {
		return nil, errProjectionRecoveryProof
	}
	delete(event, "revision")

	return event, nil
}

func projectionRecoveryFailure(err error) error {
	if typed, ok := errs.As(err); ok && typed.Code() != errs.Internal && typed.Code() != errs.Unavailable {
		return errors.New("восстановление допуска отклонено: " + typed.Message())
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}

	return errors.New("восстановление допуска не выполнено: проверьте журнал и доступность базы")
}
