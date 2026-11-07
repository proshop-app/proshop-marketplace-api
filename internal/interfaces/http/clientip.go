package http

import (
	"net"
	"net/http"
	"strings"
)

// clientIP — адрес клиента; всё, что зависит от адреса (лимиты, регион), берёт его отсюда.
// Каждый прокси дописывает в X-Forwarded-For справа адрес того, кто к нему подключился,
// поэтому верны только trustedProxies правых звеньев цепочки «X-Forwarded-For, RemoteAddr»,
// а левее лежит то, что прислал сам клиент. Клиент — звено, записанное самым дальним
// доверенным прокси. Цепочка короче — запрос пришёл изнутри установки мимо края (серверный
// рендер передаёт адрес покупателя одним звеном), клиент — самое левое звено, как у trust
// proxy с числом в Express. Без доверенных прокси заголовок не читается.
func clientIP(r *http.Request, trustedProxies int) string {
	chain := []string{r.RemoteAddr}
	if trustedProxies > 0 {
		forwarded := forwardedFor(r.Header)
		for i := len(forwarded) - 1; i >= 0 && len(chain) <= trustedProxies; i-- {
			chain = append(chain, forwarded[i])
		}
	}

	// Не адрес может прислать только клиент; тогда клиентом считается ближайшее звено правее:
	// пустой адрес выключил бы лимит, а так мусор делит счётчик с прокси, который его передал.
	for i := len(chain) - 1; i >= 0; i-- {
		if ip := hopIP(chain[i]); ip != "" {
			return ip
		}
	}

	return ""
}

// forwardedFor — звенья X-Forwarded-For слева направо по всем строкам заголовка: прокси
// может дописать свою строку, а не продолжить чужую.
func forwardedFor(header http.Header) []string {
	var hops []string
	for _, line := range header.Values("X-Forwarded-For") {
		for _, hop := range strings.Split(line, ",") {
			if hop = strings.TrimSpace(hop); hop != "" {
				hops = append(hops, hop)
			}
		}
	}

	return hops
}

// hopIP — адрес звена без порта в каноническом виде; не адрес — пустая строка.
func hopIP(hop string) string {
	if host, _, err := net.SplitHostPort(hop); err == nil {
		hop = host
	}
	if ip := net.ParseIP(hop); ip != nil {
		return ip.String()
	}

	return ""
}
