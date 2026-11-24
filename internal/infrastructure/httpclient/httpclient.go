// Package httpclient — единственная сборка HTTP-клиента процесса: свой транспорт с
// пределом соединений и сроками установки. Стандартный транспорт держит два
// простаивающих соединения на хост: после всплеска одновременных вызовов остальные
// закрываются, и следующий всплеск заново платит за TCP и TLS; сроков установки
// соединения и заголовков у него нет совсем.
package httpclient

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"time"
)

const (
	// internalConnect — соединение внутри кластера устанавливается за миллисекунды;
	// дольше секунды — сервис недоступен, вызов отказывает сразу и не съедает срок.
	internalConnect = time.Second
	// internalHandshake — рукопожатие TLS с тем же запасом на сеть кластера.
	internalHandshake = 2 * time.Second
	// externalConnect и externalHandshake — внешние сервисы через интернет (sms.ru,
	// модель синонимов, DB-IP): запас на межсетевые задержки, но не стандартные 30 с.
	externalConnect   = 5 * time.Second
	externalHandshake = 5 * time.Second
	// keepAlive — проверка живости TCP, как у стандартного транспорта.
	keepAlive = 30 * time.Second
	// idleTimeout — простаивающее соединение закрывается, как у стандартного транспорта.
	idleTimeout = 90 * time.Second
	// expectContinueTimeout — ожидание 100-continue, как у стандартного транспорта.
	expectContinueTimeout = time.Second
)

// Config — ожидаемая одновременность к сервису и сроки вызова.
type Config struct {
	// Conns — предел соединений к хосту и тёплых простаивающих: всплеск переиспользует
	// соединения, а лишние вызовы ждут соединение в пределах своего срока, не открывая
	// сокеты без предела.
	Conns int
	// Timeout — потолок вызова, включая ожидание заголовков и чтение тела.
	Timeout time.Duration
	// HeaderTimeout — ожидание заголовков ответа, если оно короче потолка вызова (долгая
	// загрузка тела). Ноль — отдельного нет: вызов со сроком в контексте (листание ПВЗ
	// сервера) законно ждёт ответ дольше потолка обычного вызова.
	HeaderTimeout time.Duration
	// RootCAs — свой удостоверяющий центр сервиса; nil — системные.
	RootCAs *x509.CertPool
	// DirectOnly — без прокси окружения: загрузка разрешена только с одного хоста напрямую.
	DirectOnly bool
}

// Internal — клиент к сервису кластера (поиск, сервер магазинов, identity-hub).
func Internal(cfg Config) *http.Client {
	return build(cfg, internalConnect, internalHandshake)
}

// External — клиент к сервису в интернете (sms.ru, модель синонимов, DB-IP).
func External(cfg Config) *http.Client {
	return build(cfg, externalConnect, externalHandshake)
}

// build собирает клиент без переходов по redirect: служебный токен не уходит на чужой адрес.
func build(cfg Config, connect, handshake time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: connect, KeepAlive: keepAlive}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          cfg.Conns,
		MaxIdleConnsPerHost:   cfg.Conns,
		MaxConnsPerHost:       cfg.Conns,
		IdleConnTimeout:       idleTimeout,
		TLSHandshakeTimeout:   handshake,
		ResponseHeaderTimeout: cfg.HeaderTimeout,
		ExpectContinueTimeout: expectContinueTimeout,
	}
	if cfg.DirectOnly {
		transport.Proxy = nil
	}
	if cfg.RootCAs != nil {
		transport.TLSClientConfig = &tls.Config{RootCAs: cfg.RootCAs, MinVersion: tls.VersionTLS12}
	}

	return &http.Client{
		Timeout:       cfg.Timeout,
		Transport:     transport,
		CheckRedirect: refuseRedirect,
	}
}

func refuseRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
