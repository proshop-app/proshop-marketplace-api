package ports

import "context"

// Message — сообщение брокеру. Топиками владеет сервер установки: издатель
// пишет только в существующий топик и не заводит его.
type Message struct {
	Topic   string
	Key     []byte
	Value   []byte
	Headers map[string]string
}

// Publisher — публикация событий площадки; вызывается из разбора outbox, а
// не из транзакции изменения, поэтому повтор после сбоя безопасен.
type Publisher interface {
	Publish(ctx context.Context, message Message) error
}
