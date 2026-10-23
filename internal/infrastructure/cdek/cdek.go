// Package cdek — справочник пунктов выдачи СДЭК, читаемый учётной записью
// платформы (API v2). Расчёт и отправления — у магазинов, здесь только список пунктов.
package cdek

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

const (
	// DefaultBaseURL — боевой адрес API.
	DefaultBaseURL = "https://api.cdek.ru"
	// Carrier — код перевозчика, как у сервера установки.
	Carrier delivery.Carrier = "cdek"
	// DefaultPageSize — пунктов на страницу выгрузки.
	DefaultPageSize = 1000
)

// ErrUnavailable — API СДЭК недоступно или отказало.
var ErrUnavailable = errs.New(errs.Unavailable, "cdek.unavailable", "справочник СДЭК недоступен")

// Config — учётная запись платформы и адрес API.
type Config struct {
	BaseURL      string
	ClientID     string
	ClientSecret string
	PageSize     int
	Timeout      time.Duration
}

// Provider — выгрузка пунктов выдачи СДЭК.
type Provider struct {
	config Config
	http   *http.Client
	now    func() time.Time

	mu      sync.Mutex
	token   string
	expires time.Time
}

// New — справочник СДЭК.
func New(cfg Config) *Provider {
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.PageSize <= 0 {
		cfg.PageSize = DefaultPageSize
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = time.Minute
	}

	return &Provider{config: cfg, http: &http.Client{Timeout: cfg.Timeout}, now: time.Now}
}

// Carrier — СДЭК.
func (p *Provider) Carrier() delivery.CarrierInfo {
	return delivery.CarrierInfo{Code: Carrier, Name: "СДЭК"}
}

type location struct {
	City        string  `json:"city"`
	Address     string  `json:"address"`
	AddressFull string  `json:"address_full"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
}

type deliveryPoint struct {
	Code     string   `json:"code"`
	Name     string   `json:"name"`
	WorkTime string   `json:"work_time"`
	Location location `json:"location"`
}

// Fetch выгружает все пункты постранично. Запись без кода, адреса или
// координат пропускается: показать её на карте нельзя.
func (p *Provider) Fetch(ctx context.Context, yield func([]delivery.PickupPoint) error) error {
	for page := 0; ; page++ {
		raw, err := p.page(ctx, page)
		if err != nil {
			return err
		}
		if len(raw) == 0 {
			return nil
		}

		now := p.now()
		points := make([]delivery.PickupPoint, 0, len(raw))

		for _, item := range raw {
			var dp deliveryPoint
			if err := json.Unmarshal(item, &dp); err != nil {
				return ErrUnavailable.Wrap(err)
			}

			address := dp.Location.AddressFull
			if address == "" {
				address = dp.Location.Address
			}

			point, err := delivery.NewPickupPoint(Carrier, dp.Code, dp.Name, address, dp.Location.City,
				delivery.Location{Lat: dp.Location.Latitude, Lon: dp.Location.Longitude}, dp.WorkTime, item, now)
			if err != nil {
				continue
			}

			points = append(points, point)
		}

		if len(points) > 0 {
			if err := yield(points); err != nil {
				return err
			}
		}

		if len(raw) < p.config.PageSize {
			return nil
		}
	}
}

func (p *Provider) page(ctx context.Context, page int) ([]json.RawMessage, error) {
	token, err := p.accessToken(ctx)
	if err != nil {
		return nil, err
	}

	query := url.Values{"type": {"ALL"}, "page": {strconv.Itoa(page)}, "size": {strconv.Itoa(p.config.PageSize)}}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.config.BaseURL+"/v2/deliverypoints?"+query.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("запрос к СДЭК: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)

	var raw []json.RawMessage
	if err := p.do(request, &raw); err != nil {
		return nil, err
	}

	return raw, nil
}

// accessToken — токен учётной записи, обновляется за минуту до конца.
func (p *Provider) accessToken(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.token != "" && p.now().Before(p.expires) {
		return p.token, nil
	}

	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {p.config.ClientID}, "client_secret": {p.config.ClientSecret}}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.config.BaseURL+"/v2/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("запрос к СДЭК: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	var answer struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := p.do(request, &answer); err != nil {
		return "", err
	}
	if answer.AccessToken == "" || answer.ExpiresIn <= 0 {
		return "", ErrUnavailable.WithMessage("СДЭК не выдал токен")
	}

	p.token = answer.AccessToken
	p.expires = p.now().Add(time.Duration(answer.ExpiresIn)*time.Second - time.Minute)

	return p.token, nil
}

func (p *Provider) do(request *http.Request, out any) error {
	reply, err := p.http.Do(request)
	if err != nil {
		return ErrUnavailable.Wrap(err)
	}
	defer func() { _ = reply.Body.Close() }()

	if reply.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(reply.Body, 512))

		return ErrUnavailable.WithMessage(fmt.Sprintf("%s: HTTP %d %s", request.URL.Path, reply.StatusCode, strings.TrimSpace(string(detail))))
	}

	if err := json.NewDecoder(reply.Body).Decode(out); err != nil {
		return ErrUnavailable.Wrap(err)
	}

	return nil
}
