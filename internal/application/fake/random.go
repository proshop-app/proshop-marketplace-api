package fake

// Random детерминированно выбирает последний элемент для сценарных и транспортных тестов.
type Random struct{}

// IntN позволяет воспроизводить выбор без глобального состояния генератора.
func (Random) IntN(n int) int { return n - 1 }
