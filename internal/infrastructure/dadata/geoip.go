// Package dadata — необязательное определение региона по IP без журналирования адреса.
package dadata

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/regions"
)

// Endpoint — адрес метода DaData iplocate/address.
const Endpoint = "https://suggestions.dadata.ru/suggestions/api/4_1/rs/iplocate/address"

// ErrUnavailable — безопасная ошибка без URL и адреса покупателя.
var ErrUnavailable = errs.New(errs.Unavailable, "geoip.unavailable", "геолокация недоступна")

// GeoIP — определяет регион по адресу без сохранения его в профиле.
type GeoIP struct {
	key      string
	endpoint string
	client   *http.Client
}

type response struct {
	Location *struct {
		Data struct {
			RegionISO string `json:"region_iso_code"`
		} `json:"data"`
	} `json:"location"`
}

// New — создаёт клиент с ограниченным временем запроса.
func New(key, endpoint string) *GeoIP {
	return &GeoIP{key: key, endpoint: endpoint, client: &http.Client{Timeout: 3 * time.Second}}
}

// Locate — возвращает регион или nil при отсутствии подсказки.
func (g *GeoIP) Locate(ctx context.Context, ip string) (*buyer.Region, error) {
	address := net.ParseIP(ip)
	if g.key == "" || address == nil || !address.IsGlobalUnicast() || address.IsPrivate() {
		return nil, nil
	}
	endpoint, err := url.Parse(g.endpoint)
	if err != nil {
		return nil, ErrUnavailable
	}
	query := endpoint.Query()
	query.Set("ip", address.String())
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, ErrUnavailable
	}
	req.Header.Set("Authorization", "Token "+g.key)
	req.Header.Set("Accept", "application/json")
	resp, err := g.client.Do(req)
	// Ошибки net/http содержат URL с IP: в журнал и наружу — только вид сбоя.
	if err != nil {
		return nil, unavailable("transport", 0)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, unavailable("status", resp.StatusCode)
	}
	var body response
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil, unavailable("decode", resp.StatusCode)
	}
	if body.Location == nil {
		return nil, nil
	}
	code := body.Location.Data.RegionISO
	name, ok := regions.Name(code)
	if !ok {
		return nil, nil
	}
	return &buyer.Region{Code: code, Name: name}, nil
}

// unavailable пишет вид сбоя без IP и ключа: оператор видит неверный ключ или исчерпанную квоту.
func unavailable(kind string, status int) error {
	slog.Warn("dadata: геолокация недоступна", slog.String("kind", kind), slog.Int("status", status))
	return ErrUnavailable
}
