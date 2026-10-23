package ids_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestParseTenantID(t *testing.T) {
	for _, valid := range []string{"t1", "6720a1b2c3d4e5f6a7b8c9d0", "shop.main:1_x-y"} {
		if _, err := ids.ParseTenantID(valid); err != nil {
			t.Fatalf("%q: %v", valid, err)
		}
	}

	for _, invalid := range []string{"", "-lead", "with space", "кириллица", "a~b", strings.Repeat("a", 129)} {
		if _, err := ids.ParseTenantID(invalid); !errors.Is(err, ids.ErrInvalid) {
			t.Fatalf("%q должен быть отвергнут, получено %v", invalid, err)
		}
	}
}

func TestParseProductAndTag(t *testing.T) {
	if _, err := ids.ParseProductID("p-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := ids.ParseTagID("tag:1"); err != nil {
		t.Fatal(err)
	}
	if _, err := ids.ParseProductID("bad id"); err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if _, err := ids.ParseTagID(""); err == nil {
		t.Fatal("ожидалась ошибка")
	}
}

func TestOfferIDRoundTrip(t *testing.T) {
	offer := ids.OfferID{Tenant: "t1", Product: "p.1"}
	if offer.String() != "t1~p.1" {
		t.Fatalf("запись %q", offer.String())
	}

	parsed, err := ids.ParseOfferID(offer.String())
	if err != nil || parsed != offer {
		t.Fatalf("разбор %+v, %v", parsed, err)
	}

	for _, invalid := range []string{"t1", "~p", "t1~", "t1~p~x", "t 1~p"} {
		if _, err := ids.ParseOfferID(invalid); err == nil {
			t.Fatalf("%q должен быть отвергнут", invalid)
		}
	}
}

func TestParseUUID(t *testing.T) {
	if _, err := ids.ParseUUID("6f1c2b1e-3c4d-4e5f-8a9b-0c1d2e3f4a5b"); err != nil {
		t.Fatal(err)
	}

	invalid := []string{
		"",
		"6F1C2B1E-3C4D-4E5F-8A9B-0C1D2E3F4A5B",
		"6f1c2b1e3c4d4e5f8a9b0c1d2e3f4a5b",
		"../../etc",
	}

	for _, invalid := range invalid {
		if _, err := ids.ParseUUID(invalid); !errors.Is(err, ids.ErrInvalid) {
			t.Fatalf("%q должен быть отвергнут", invalid)
		}
	}
}
