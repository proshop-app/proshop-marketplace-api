// Package platformapi — клиент внутреннего API сервера установки для
// маркетплейса (контракт PR-M1, docs/server-contract.md): служебный токен в
// x-internal-token, магазин в пути, суммы — целые копейки.
package platformapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// HeaderToken — заголовок служебного токена, как у сервера.
const HeaderToken = "x-internal-token"

var (
	// ErrUnavailable — сервер недоступен или отказал.
	ErrUnavailable = errs.New(errs.Unavailable, "platform.unavailable", "сервер магазина недоступен")
	// ErrInvalidResponse — ответ сервера не по контракту.
	ErrInvalidResponse = errs.New(errs.Unavailable, "platform.invalid_response", "ответ сервера магазина не по контракту")
)

// Config — адрес сервера, токен маркетплейса и срок ожидания.
type Config struct {
	BaseURL string
	Token   string
	Timeout time.Duration
}

// Client — внутренний API сервера.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New — клиент API сервера.
func New(cfg Config) *Client {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	return &Client{baseURL: strings.TrimRight(cfg.BaseURL, "/"), token: cfg.Token, http: &http.Client{Timeout: timeout}}
}

type itemJSON struct {
	ProductID string `json:"productId"`
	Quantity  int    `json:"quantity"`
}

type itemCheckJSON struct {
	ProductID         string `json:"productId"`
	Available         bool   `json:"available"`
	Price             int64  `json:"price"`
	AvailableQuantity int    `json:"availableQuantity"`
}

// CheckItems — цена и наличие позиций у магазина.
func (c *Client) CheckItems(ctx context.Context, tenantID ids.TenantID, items []ports.ItemRequest) ([]ports.ItemCheck, error) {
	var answer struct {
		Items []itemCheckJSON `json:"items"`
	}

	if err := c.call(ctx, http.MethodPost, tenantID, "/items/check", map[string]any{"items": itemsJSON(items)}, &answer); err != nil {
		return nil, err
	}

	checks := make([]ports.ItemCheck, 0, len(answer.Items))
	for _, item := range answer.Items {
		product, err := ids.ParseProductID(item.ProductID)
		if err != nil || item.Price < 0 || item.AvailableQuantity < 0 {
			return nil, ErrInvalidResponse.WithMessage("позиция ответа не по контракту: " + item.ProductID)
		}

		checks = append(checks, ports.ItemCheck{
			ProductID: product, Exists: item.Available, Price: money.FromKopecks(item.Price), Available: item.AvailableQuantity,
		})
	}

	return checks, nil
}

type quoteJSON struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
	Cost      int64  `json:"cost"`
	MinDays   int    `json:"minDays"`
	MaxDays   int    `json:"maxDays"`
	ExpiresAt string `json:"expiresAt"`
}

// QuoteDelivery — стоимость и срок доставки до пункта по договору магазина.
func (c *Client) QuoteDelivery(ctx context.Context, tenantID ids.TenantID, request ports.QuoteRequest) (delivery.Offered, error) {
	body := map[string]any{
		"carrier": string(request.Carrier), "pickupPointCode": request.PickupPointCode, "city": request.City, "items": itemsJSON(request.Items),
	}

	var answer quoteJSON
	if err := c.call(ctx, http.MethodPost, tenantID, "/delivery/quote", body, &answer); err != nil {
		return delivery.Offered{}, err
	}

	offered := delivery.Offered{
		Available: answer.Available, Reason: answer.Reason, Cost: money.FromKopecks(answer.Cost), MinDays: answer.MinDays, MaxDays: answer.MaxDays,
	}

	if answer.ExpiresAt != "" {
		expires, err := time.Parse(time.RFC3339, answer.ExpiresAt)
		if err != nil {
			return delivery.Offered{}, ErrInvalidResponse.WithMessage("expiresAt не в RFC 3339")
		}

		offered.ExpiresAt = expires
	}

	return offered, nil
}

type settingsJSON struct {
	Enabled bool `json:"enabled"`
	Subsidy *struct {
		Enabled   bool   `json:"enabled"`
		Type      string `json:"type"`
		Value     int64  `json:"value"`
		MaxAmount int64  `json:"maxAmount"`
	} `json:"subsidy"`
}

// MarketplaceSettings — флаг участия и субсидия из настроек магазина.
func (c *Client) MarketplaceSettings(ctx context.Context, tenantID ids.TenantID) (ports.ShopSettings, error) {
	var answer settingsJSON
	if err := c.call(ctx, http.MethodGet, tenantID, "/settings/marketplace", nil, &answer); err != nil {
		return ports.ShopSettings{}, err
	}

	settings := ports.ShopSettings{Enabled: answer.Enabled}

	if s := answer.Subsidy; s != nil {
		settings.Subsidy = participant.Subsidy{
			Enabled: s.Enabled, Type: participant.SubsidyType(s.Type), Value: s.Value, MaxAmount: money.FromKopecks(s.MaxAmount),
		}
	}

	if err := settings.Subsidy.Validate(); err != nil {
		return ports.ShopSettings{}, ErrInvalidResponse.Wrap(err)
	}

	return settings, nil
}

func (c *Client) call(ctx context.Context, method string, tenantID ids.TenantID, path string, payload any, out any) error {
	var body io.Reader

	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("запрос к серверу: %w", err)
		}

		body = bytes.NewReader(encoded)
	}

	endpoint := c.baseURL + "/api/v1/internal/marketplace/tenants/" + url.PathEscape(string(tenantID)) + path

	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return fmt.Errorf("запрос к серверу: %w", err)
	}

	request.Header.Set(HeaderToken, c.token)
	request.Header.Set("Accept", "application/json")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	reply, err := c.http.Do(request)
	if err != nil {
		return ErrUnavailable.Wrap(err)
	}
	defer func() { _ = reply.Body.Close() }()

	if reply.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(reply.Body, 512))

		return ErrUnavailable.WithMessage(fmt.Sprintf("%s %s: HTTP %d %s", method, path, reply.StatusCode, strings.TrimSpace(string(detail))))
	}

	if err := json.NewDecoder(io.LimitReader(reply.Body, 4<<20)).Decode(out); err != nil {
		return ErrInvalidResponse.Wrap(err)
	}

	return nil
}

func itemsJSON(items []ports.ItemRequest) []itemJSON {
	out := make([]itemJSON, 0, len(items))
	for _, item := range items {
		out = append(out, itemJSON{ProductID: string(item.ProductID), Quantity: item.Quantity})
	}

	return out
}
