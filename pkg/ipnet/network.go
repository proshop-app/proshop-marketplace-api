// Package ipnet сводит адрес клиента к его сети: кэш и лимиты по сети не хранят сам адрес
// и не распадаются на соседние адреса одного провайдера.
package ipnet

import "net/netip"

// Network объединяет адреса клиента в /24 IPv4 или /64 IPv6.
func Network(ip string) string {
	address, err := netip.ParseAddr(ip)
	if err != nil {
		return ""
	}
	address = address.Unmap()
	bits := 64
	if address.Is4() {
		bits = 24
	}
	return netip.PrefixFrom(address, bits).Masked().String()
}
