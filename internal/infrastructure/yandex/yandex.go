// Package yandex — справочник пунктов выдачи Яндекс Доставки, читаемый
// учётной записью платформы (платформенное API b2b). Расчёт и отправления —
// у магазинов, здесь только список пунктов.
package yandex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

const (
	// DefaultBaseURL — боевой адрес платформенного API.
	DefaultBaseURL = "https://b2b-authproxy.taxi.yandex.net"
	// Carrier — код перевозчика, как у сервера установки.
	Carrier delivery.Carrier = "yandex"
	// batchSize — пунктов в одной пачке для записи.
	batchSize = 1000
)

// ErrUnavailable — API Яндекс Доставки недоступно или отказало.
var ErrUnavailable = errs.New(errs.Unavailable, "yandex.unavailable", "справочник Яндекс Доставки недоступен")

// Config — токен учётной записи платформы и адрес API.
type Config struct {
	BaseURL string
	Token   string
	Timeout time.Duration
}

// Provider — выгрузка пунктов выдачи Яндекс Доставки.
type Provider struct {
	config Config
	http   *http.Client
	now    func() time.Time
}

type pickupPoint struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Address struct {
		FullAddress string `json:"full_address"`
		Locality    string `json:"locality"`
	} `json:"address"`
	Position struct {
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
	} `json:"position"`
	Schedule struct {
		Restrictions []restriction `json:"restrictions"`
	} `json:"schedule"`
}

type clock struct {
	Hours   int `json:"hours"`
	Minutes int `json:"minutes"`
}

type restriction struct {
	Days     []int `json:"days"`
	TimeFrom clock `json:"time_from"`
	TimeTo   clock `json:"time_to"`
}

var weekdays = []string{"", "Пн", "Вт", "Ср", "Чт", "Пт", "Сб", "Вс"}

// New — справочник Яндекс Доставки.
func New(cfg Config) *Provider {
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Minute
	}

	return &Provider{config: cfg, http: &http.Client{Timeout: cfg.Timeout}, now: time.Now}
}

// Carrier — Яндекс Доставка.
func (p *Provider) Carrier() delivery.CarrierInfo {
	return delivery.CarrierInfo{Code: Carrier, Name: "Яндекс Доставка"}
}

// Fetch выгружает пункты выдачи одним запросом и разбирает ответ потоком:
// список большой, в память целиком он не поднимается.
func (p *Provider) Fetch(ctx context.Context, yield func([]delivery.PickupPoint) error) error {
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		p.config.BaseURL+"/api/b2b/platform/pickup-points/list",
		bytes.NewReader([]byte(`{"type":"pickup_point"}`)),
	)
	if err != nil {
		return fmt.Errorf("запрос к Яндекс Доставке: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+p.config.Token)
	request.Header.Set("Content-Type", "application/json")
	// без языка часть ответов приходит по-английски
	request.Header.Set("Accept-Language", "ru")

	reply, err := p.http.Do(request)
	if err != nil {
		return ErrUnavailable.Wrap(err)
	}
	defer func() { _ = reply.Body.Close() }()

	if reply.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(reply.Body, 512))

		message := fmt.Sprintf("pickup-points/list: HTTP %d %s", reply.StatusCode, strings.TrimSpace(string(detail)))

		return ErrUnavailable.WithMessage(message)
	}

	decoder := json.NewDecoder(reply.Body)
	if err := seekArray(decoder, "points"); err != nil {
		return err
	}

	now := p.now()
	batch := make([]delivery.PickupPoint, 0, batchSize)

	for decoder.More() {
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return ErrUnavailable.Wrap(err)
		}

		var pp pickupPoint
		if err := json.Unmarshal(raw, &pp); err != nil {
			return ErrUnavailable.Wrap(err)
		}

		point, err := delivery.NewPickupPoint(
			Carrier,
			pp.ID,
			pp.Name,
			pp.Address.FullAddress,
			pp.Address.Locality,
			delivery.Location{
				Lat: pp.Position.Latitude,
				Lon: pp.Position.Longitude,
			},
			schedule(pp.Schedule.Restrictions),
			raw,
			now,
		)
		if err != nil {
			continue
		}

		if batch = append(batch, point); len(batch) == batchSize {
			if err := yield(batch); err != nil {
				return err
			}

			batch = make([]delivery.PickupPoint, 0, batchSize)
		}
	}

	if len(batch) > 0 {
		return yield(batch)
	}

	return nil
}

// seekArray доводит декодер до начала массива key в корневом объекте.
func seekArray(decoder *json.Decoder, key string) error {
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return ErrUnavailable.WithMessage("ответ — не объект")
	}

	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return ErrUnavailable.Wrap(err)
		}

		if token == key {
			if open, err := decoder.Token(); err != nil || open != json.Delim('[') {
				return ErrUnavailable.WithMessage("список пунктов — не массив")
			}

			return nil
		}

		var skip json.RawMessage
		if err := decoder.Decode(&skip); err != nil {
			return ErrUnavailable.Wrap(err)
		}
	}

	return ErrUnavailable.WithMessage("в ответе нет списка пунктов")
}

// schedule — график словами: «Пн-Пт 10:00-21:00; Сб-Вс 10:30-18:00».
func schedule(restrictions []restriction) string {
	parts := make([]string, 0, len(restrictions))

	for _, r := range restrictions {
		if len(r.Days) == 0 {
			continue
		}

		days := dayName(r.Days[0])
		if last := r.Days[len(r.Days)-1]; last != r.Days[0] {
			days += "-" + dayName(last)
		}

		parts = append(
			parts,
			fmt.Sprintf(
				"%s %02d:%02d-%02d:%02d",
				days,
				r.TimeFrom.Hours,
				r.TimeFrom.Minutes,
				r.TimeTo.Hours,
				r.TimeTo.Minutes,
			),
		)
	}

	return strings.Join(parts, "; ")
}

func dayName(day int) string {
	if day < 1 || day >= len(weekdays) {
		return fmt.Sprintf("день %d", day)
	}

	return weekdays[day]
}
