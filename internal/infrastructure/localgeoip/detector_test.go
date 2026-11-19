package localgeoip

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/maxmind/mmdbwriter"
	"github.com/maxmind/mmdbwriter/mmdbtype"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

func database(t testing.TB, lat, lon float64) []byte {
	t.Helper()
	tree, err := mmdbwriter.New(mmdbwriter.Options{
		DatabaseType: "DBIP-City-Lite",
		IPVersion:    6,
		Description:  map[string]string{"en": "test fixture"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for ip, country := range map[string]string{
		"77.88.8.8/32": "RU",
		"8.8.8.8/32":   "US",
		// Как в опубликованном DB-IP 2026-11: Google IPv6 anycast находится в CA.
		"2001:4860:4860::8888/128": "CA",
	} {
		_, prefix, _ := net.ParseCIDR(ip)
		if err := tree.Insert(prefix, mmdbtype.Map{
			"country": mmdbtype.Map{"iso_code": mmdbtype.String(country)},
			"location": mmdbtype.Map{
				"latitude":  mmdbtype.Float64(lat),
				"longitude": mmdbtype.Float64(lon),
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	if _, err := tree.WriteTo(&output); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func detector(t testing.TB, path string, dev netip.Addr) *Detector {
	t.Helper()
	d, err := New(path, dev, slog.New(slog.NewTextHandler(io.Discard, nil)), prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestLocalDetectionReturnsCanonicalCityAndNoResultForUnknownOrForeign(t *testing.T) {
	path := filepath.Join(t.TempDir(), "city.mmdb")
	if err := os.WriteFile(path, database(t, 55.7540471, 37.620405), 0600); err != nil {
		t.Fatal(err)
	}
	d := detector(t, path, netip.Addr{})
	result, err := d.DetectCity(context.Background(), "77.88.8.8")
	if err != nil || result.City != "Москва" || result.RegionCode != "RU-MOW" || result.FiasID == "" ||
		result.KladrID != "7700000000000" {
		t.Fatal(result, err)
	}
	for _, ip := range []string{"8.8.8.8", "2001:4860:4860::8888", "1.1.1.1", "invalid"} {
		result, err := d.DetectCity(context.Background(), ip)
		if err != nil || result.City != "" {
			t.Fatal(ip, result, err)
		}
	}
}

func TestPrivateAndSpecialAddressesSkipMissingDatabase(t *testing.T) {
	d := detector(t, filepath.Join(t.TempDir(), "missing"), netip.Addr{})
	for _, ip := range []string{
		"10.1.2.3",
		"172.16.1.2",
		"192.168.1.1",
		"127.0.0.1",
		"100.64.0.1",
		"169.254.1.2",
		"::1",
		"fc00::1",
		"fe80::1",
		"::ffff:192.168.1.2",
		"192.0.2.1",
		"2001:db8::1",
	} {
		result, err := d.DetectCity(context.Background(), ip)
		if err != nil || result.City != "" {
			t.Fatal(ip, result, err)
		}
	}
	if _, err := d.DetectCity(context.Background(), "77.88.8.8"); !errors.Is(err, ports.ErrCityUnavailable) {
		t.Fatal(err)
	}
}

func TestAtomicReplacementKeepsConcurrentReadersAndRejectsBrokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "city.mmdb")
	if err := os.WriteFile(path, database(t, 55.7540471, 37.620405), 0600); err != nil {
		t.Fatal(err)
	}
	d := detector(t, path, netip.MustParseAddr("77.88.8.8"))
	if result, err := d.DetectCity(context.Background(), "10.1.2.3"); err != nil || result.City != "Москва" {
		t.Fatal(result, err)
	}
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for i := 0; i < 100; i++ {
				result, err := d.DetectCity(context.Background(), "77.88.8.8")
				if err != nil || (result.City != "Москва" && result.City != "Санкт-Петербург") {
					t.Error(result, err)
				}
			}
		}()
	}
	next := path + ".next"
	if err := os.WriteFile(next, database(t, 59.9391313, 30.3159004), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(next, path); err != nil {
		t.Fatal(err)
	}
	d.Reload()
	group.Wait()
	if err := os.WriteFile(next, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(next, path); err != nil {
		t.Fatal(err)
	}
	d.Reload()
	result, err := d.DetectCity(context.Background(), "77.88.8.8")
	if err != nil || result.City != "Санкт-Петербург" {
		t.Fatal(result, err)
	}
}

func TestUpdaterAcceptsLocatedAnycastAndKeepsPreviousOnInvalidReplacement(t *testing.T) {
	data := database(t, 55.7540471, 37.620405)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		zipped := gzip.NewWriter(w)
		_, _ = zipped.Write(data)
		_ = zipped.Close()
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "city.mmdb")
	updater := &Updater{client: server.Client(), source: server.URL + "/"}
	if err := updater.Update(context.Background(), path, "2026-11"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	data = []byte("bad MMDB")
	if err := updater.Update(context.Background(), path, "2026-11"); err == nil {
		t.Fatal("невалидная база принята")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("прежняя база изменена")
	}
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".dbip-*"))
	if len(files) != 0 {
		t.Fatal(files)
	}
}

func BenchmarkDetectCity(b *testing.B) {
	path := filepath.Join(b.TempDir(), "city.mmdb")
	if err := os.WriteFile(path, database(b, 55.7540471, 37.620405), 0600); err != nil {
		b.Fatal(err)
	}
	d := detector(b, path, netip.Addr{})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = d.DetectCity(context.Background(), "77.88.8.8")
	}
}
