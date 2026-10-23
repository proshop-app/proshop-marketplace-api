package errs_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

var errSample = errs.New(errs.NotFound, "sample.not_found", "нет образца")

func TestIsMatchesByKey(t *testing.T) {
	refined := errSample.WithMessage("нет образца 42")
	if !errors.Is(refined, errSample) {
		t.Fatal("ошибка с уточнённым сообщением должна совпадать с исходной")
	}

	other := errs.New(errs.NotFound, "other.not_found", "нет другого")
	if errors.Is(other, errSample) {
		t.Fatal("ошибки с разными ключами не должны совпадать")
	}
}

func TestWrapKeepsCauseAndIdentity(t *testing.T) {
	cause := errors.New("сбой")
	wrapped := fmt.Errorf("контекст: %w", errSample.Wrap(cause))

	if !errors.Is(wrapped, errSample) {
		t.Fatal("обёрнутая ошибка должна совпадать по ключу")
	}
	if !errors.Is(wrapped, cause) {
		t.Fatal("причина должна быть доступна через errors.Is")
	}
}

func TestCodeOf(t *testing.T) {
	if got := errs.CodeOf(fmt.Errorf("x: %w", errSample)); got != errs.NotFound {
		t.Fatalf("код %q, ожидался not_found", got)
	}
	if got := errs.CodeOf(errors.New("plain")); got != errs.Internal {
		t.Fatalf("код %q, ожидался internal", got)
	}
}

func TestAs(t *testing.T) {
	typed, ok := errs.As(fmt.Errorf("x: %w", errSample))
	if !ok || typed.Key() != "sample.not_found" || typed.Message() != "нет образца" || typed.Code() != errs.NotFound {
		t.Fatalf("неожиданная ошибка: %+v", typed)
	}
	if _, ok := errs.As(errors.New("plain")); ok {
		t.Fatal("обычная ошибка не типизирована")
	}
}
