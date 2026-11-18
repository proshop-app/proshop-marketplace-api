// Package ipapi повторяет источник определения города proshop-base/server/api/geo.get.ts.
package ipapi

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

// Endpoint и Timeout — тот же источник и бюджет, что у витрины магазина.
const Endpoint = "http://ip-api.com/json"

// Timeout повторяет срок определения города в proshop-base.
const Timeout = 1500 * time.Millisecond

// ErrUnavailable оставляет покупателю ручной выбор города.
var ErrUnavailable = errs.New(errs.Unavailable, "city.unavailable", "не удалось определить город, выберите его вручную")

// Detector использует тот же IP-источник, что витрина магазина.
type Detector struct {
	endpoint string
	client   *http.Client
}

type response struct {
	Status  string `json:"status"`
	City    string `json:"city"`
	Country string `json:"countryCode"`
}

// New создаёт клиент; пустой endpoint выбирает источник витрины магазина.
func New(endpoint string) *Detector {
	if endpoint == "" {
		endpoint = Endpoint
	}
	return &Detector{endpoint: strings.TrimRight(endpoint, "/"), client: &http.Client{Timeout: Timeout}}
}

// DetectCity не передаёт частный адрес внешнему источнику.
func (d *Detector) DetectCity(ctx context.Context, ip string) (ports.DetectedCity, error) {
	target := d.endpoint
	// Как в base: локальный адрес определяет внешнее местоположение самого сервера.
	if address := net.ParseIP(ip); address != nil && address.IsGlobalUnicast() && !address.IsPrivate() {
		target += "/" + address.String()
	}
	target += "?fields=status,city,countryCode&lang=ru"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return ports.DetectedCity{}, ErrUnavailable
	}
	reply, err := d.client.Do(request)
	if err != nil {
		return ports.DetectedCity{}, ErrUnavailable
	}
	defer func() { _ = reply.Body.Close() }()
	if reply.StatusCode != http.StatusOK {
		return ports.DetectedCity{}, ErrUnavailable
	}
	var body response
	if err := json.NewDecoder(io.LimitReader(reply.Body, 1<<20)).Decode(&body); err != nil {
		return ports.DetectedCity{}, ErrUnavailable
	}
	if body.Status != "success" {
		return ports.DetectedCity{}, nil
	}
	return ports.DetectedCity{City: body.City, Country: body.Country}, nil
}
