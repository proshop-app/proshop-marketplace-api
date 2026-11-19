package ipnet

import "net/netip"

// Специальные диапазоны IANA, которые IsGlobalUnicast не исключает самостоятельно.
// https://www.iana.org/assignments/iana-ipv4-special-registry/iana-ipv4-special-registry.xhtml
// https://www.iana.org/assignments/iana-ipv6-special-registry/iana-ipv6-special-registry.xhtml
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

var special = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	cgnat,
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
}

// Public исключает частные, служебные и невалидные адреса до поиска в базе.
func Public(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() {
		return false
	}
	for _, prefix := range special {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

// Private — только локальные адреса прокси/разработки; зарезервированные адреса не подменяются.
func Private(addr netip.Addr) bool {
	return addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() ||
		cgnat.Contains(addr.Unmap())
}
