package config

import (
	"net/netip"

	"github.com/proshop-app/proshop-marketplace/pkg/ipnet"
)

// City — опубликованный локальный справочник; подмена клиента разрешена только в development.
type City struct {
	Path        string
	DevClientIP netip.Addr
}

func (r *reader) city() City {
	mode := r.oneOf("MARKETPLACE_ENV", "production", "production", "development")
	result := City{Path: r.str("MARKETPLACE_CITY_DB_PATH", "/geoip/dbip-city-lite.mmdb")}
	value := r.str("MARKETPLACE_CITY_DEV_CLIENT_IP", "")
	if value == "" {
		return result
	}
	if mode != "development" {
		r.fail("MARKETPLACE_CITY_DEV_CLIENT_IP", "запрещена вне MARKETPLACE_ENV=development")
	}
	addr, err := netip.ParseAddr(value)
	if err != nil || !ipnet.Public(addr) {
		r.fail("MARKETPLACE_CITY_DEV_CLIENT_IP", "нужен публичный адрес")
	} else {
		result.DevClientIP = addr.Unmap()
	}
	return result
}
