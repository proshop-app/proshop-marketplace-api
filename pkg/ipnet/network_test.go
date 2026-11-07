package ipnet_test

import (
	"testing"

	"github.com/proshop-app/proshop-marketplace/pkg/ipnet"
)

func TestNetwork(t *testing.T) {
	for ip, want := range map[string]string{
		"192.0.2.1":            "192.0.2.0/24",
		"192.0.2.254":          "192.0.2.0/24",
		"::ffff:192.0.2.1":     "192.0.2.0/24",
		"2001:db8:1:2::1":      "2001:db8:1:2::/64",
		"2001:db8:1:2:abcd::1": "2001:db8:1:2::/64",
		"invalid":              "",
	} {
		if got := ipnet.Network(ip); got != want {
			t.Errorf("%s: %s != %s", ip, got, want)
		}
	}
}
