// Package httpclient — HTTP-клиент к внутренним сервисам установки (общий поиск,
// сервер магазинов) со своим транспортом. Стандартный транспорт держит два
// простаивающих соединения на хост: после всплеска одновременных вызовов остальные
// закрываются, и следующий всплеск заново платит за TCP и TLS.
package httpclient

import (
	"net"
	"net/http"
	"time"
)

const (
	// connectTimeout — соединение внутри кластера устанавливается за миллисекунды;
	// дольше секунды — сервис недоступен, вызов отказывает сразу и не съедает срок.
	connectTimeout = time.Second
	// tlsHandshakeTimeout — рукопожатие TLS с тем же запасом на сеть кластера.
	tlsHandshakeTimeout = 2 * time.Second
	// keepAlive — проверка живости TCP, как у стандартного транспорта.
	keepAlive = 30 * time.Second
	// idleTimeout — простаивающее соединение закрывается, как у стандартного транспорта.
	idleTimeout = 90 * time.Second
	// expectContinueTimeout — ожидание 100-continue, как у стандартного транспорта.
	expectContinueTimeout = time.Second
)

// Config — ожидаемая одновременность к сервису и потолок одного вызова.
type Config struct {
	// Conns — предел соединений к хосту и тёплых простаивающих: всплеск переиспользует
	// соединения, а лишние вызовы ждут соединение в пределах своего срока, не открывая
	// сокеты без предела.
	Conns int
	// Timeout — потолок вызова, включая ожидание заголовков и чтение тела. Отдельного
	// ResponseHeaderTimeout у транспорта нет намеренно: вызов со сроком в контексте
	// (листание ПВЗ сервера) законно ждёт ответ дольше потолка обычного вызова.
	Timeout time.Duration
}

// New — клиент без переходов по redirect: служебный токен не уходит на чужой адрес.
func New(cfg Config) *http.Client {
	dialer := &net.Dialer{Timeout: connectTimeout, KeepAlive: keepAlive}

	return &http.Client{
		Timeout: cfg.Timeout,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           dialer.DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          cfg.Conns,
			MaxIdleConnsPerHost:   cfg.Conns,
			MaxConnsPerHost:       cfg.Conns,
			IdleConnTimeout:       idleTimeout,
			TLSHandshakeTimeout:   tlsHandshakeTimeout,
			ExpectContinueTimeout: expectContinueTimeout,
		},
		CheckRedirect: refuseRedirect,
	}
}

func refuseRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
