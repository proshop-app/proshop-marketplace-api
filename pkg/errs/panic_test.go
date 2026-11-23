package errs_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

func TestGuardTurnsPanicIntoInternalErrorWithStack(t *testing.T) {
	err := errs.Guard(func() error { panic(errSample) })

	if !errors.Is(err, errs.ErrPanic) || errs.CodeOf(err) != errs.Internal {
		t.Fatalf("паника — внутренняя ошибка, а не класс значения паники: %v", err)
	}
	if errs.RetryKindOf(err) != errs.RetryUnknown {
		t.Fatal("паника считается неизвестным сбоем: повтор, затем исчерпание попыток")
	}
	stack, ok := errs.PanicStack(err)
	if !ok || !strings.Contains(string(stack), "TestGuardTurnsPanicIntoInternalErrorWithStack") {
		t.Fatalf("стек должен вести к месту паники:\n%s", stack)
	}
	if strings.Contains(err.Error(), "goroutine") {
		t.Fatalf("стек не попадает в текст ошибки, который сохраняется в базе: %q", err.Error())
	}
}

func TestGuardKeepsOrdinaryResult(t *testing.T) {
	if err := errs.Guard(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	err := errs.Guard(func() error { return context.Canceled })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, ok := errs.PanicStack(err); ok {
		t.Fatal("обычная ошибка не несёт стек паники")
	}
}
