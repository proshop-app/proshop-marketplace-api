// Package safego — единственный способ запустить горутину в коде процесса. Паника
// в чужой горутине не ловится recover вызывающего и роняет весь процесс: один
// кривой ответ магазина остановил бы покупателей всех магазинов. Здесь паника
// превращается в errs.ErrPanic, уходит в журнал со стеком и метрику, а ожидающий
// итога получает её как ошибку сразу, а не по сроку.
package safego

import (
	"log/slog"
	"sync/atomic"

	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

// Reporter получает панику горутины по имени: журнал со стеком и метрика процесса.
type Reporter func(name string, err error)

var reporter atomic.Pointer[Reporter]

// SetReporter задаёт получателя паник процесса; сборка процесса ставит его один раз
// на старте, до запуска горутин. nil — журнал по умолчанию.
func SetReporter(report Reporter) {
	if report == nil {
		reporter.Store(nil)
		return
	}
	reporter.Store(&report)
}

// Go запускает горутину без ожидающих итога; всё, чего ждут другие (WaitGroup,
// освобождение слота), fn снимает в defer — defer выполняется и при панике.
func Go(name string, fn func()) {
	run := func() error {
		fn()
		return nil
	}
	Run(name, run, nil)
}

// Run запускает горутину, чей итог ждут: finish вызывается всегда — с ошибкой fn
// или с errs.ErrPanic — и закрывает канал или рейс ожидающих.
func Run(name string, fn func() error, finish func(error)) {
	go func() {
		err := errs.Guard(fn)
		if _, panicked := errs.PanicStack(err); panicked {
			report(name, err)
		}
		if finish == nil {
			return
		}
		if failure := errs.Guard(func() error {
			finish(err)
			return nil
		}); failure != nil {
			report(name, failure)
		}
	}()
}

// Guard — тело для чужого запускателя горутин (errgroup.Group.Go): он панику не
// перехватывает, и она уронила бы процесс. Паника уходит получателю и возвращается
// ошибкой errs.ErrPanic, как обычный отказ этого тела.
func Guard(name string, fn func() error) func() error {
	return func() error {
		err := errs.Guard(fn)
		if _, panicked := errs.PanicStack(err); panicked {
			report(name, err)
		}
		return err
	}
}

// GuardValue — то же для тела со значением (singleflight.Group.DoChan): рейс получает
// ошибку паники и закрывается для всех ожидающих.
func GuardValue[T any](name string, fn func() (T, error)) func() (T, error) {
	return func() (T, error) {
		var value T
		err := Guard(name, func() error {
			var err error
			value, err = fn()
			return err
		})()
		return value, err
	}
}

func report(name string, err error) {
	if current := reporter.Load(); current != nil {
		(*current)(name, err)
		return
	}
	stack, _ := errs.PanicStack(err)
	slog.Error(
		"горутина: паника",
		slog.String("goroutine", name),
		slog.Any("error", err),
		slog.String("stack", string(stack)),
	)
}
