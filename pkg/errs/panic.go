package errs

import (
	"errors"
	"fmt"
	"runtime/debug"
)

// ErrPanic — обработчик паниковал. Класс internal: причина неизвестна, поэтому
// исполнитель считает её неизвестным сбоем — повторяет и после исчерпания попыток
// откладывает сообщение или задачу, а не роняет процесс.
var ErrPanic = New(Internal, "runtime.panic", "паника обработчика")

// panicCause держит стек отдельно от текста: текст ошибки сохраняется в базе
// (последняя ошибка задачи, отравленное сообщение), стек нужен только журналу.
// Unwrap нет намеренно: ошибка из значения паники не меняет класс internal.
type panicCause struct {
	value any
	stack []byte
}

// Guard выполняет fn и превращает его панику в ErrPanic со стеком. Вызывается
// в той горутине, где работает обработчик: recover чужой горутины не ловит.
func Guard(fn func() error) (err error) {
	defer func() {
		if value := recover(); value != nil {
			err = ErrPanic.Wrap(&panicCause{value: value, stack: debug.Stack()})
		}
	}()

	return fn()
}

// PanicStack — стек паники, если ошибка получена из Guard.
func PanicStack(err error) ([]byte, bool) {
	var cause *panicCause
	if errors.As(err, &cause) {
		return cause.stack, true
	}

	return nil, false
}

func (p *panicCause) Error() string { return fmt.Sprint(p.value) }
