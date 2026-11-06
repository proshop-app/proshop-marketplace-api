// Package platformapi — клиент внутреннего API сервера установки для
// маркетплейса (контракт PR-M1, docs/server-contract.md): служебный токен в
// x-internal-token, магазин в пути, суммы — целые копейки.
package platformapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

const (
	// HeaderToken — заголовок служебного токена, как у сервера.
	HeaderToken = "x-internal-token"

	// codeMarketplaceDisabled — код отказа сервера для магазина, который не участвует в маркетплейсе.
	codeMarketplaceDisabled = "MARKETPLACE_DISABLED"
)

var (
	// ErrUnavailable — сервер недоступен или отказал.
	ErrUnavailable = errs.New(errs.Unavailable, "platform.unavailable", "сервер магазина недоступен")
	// ErrInvalidResponse — ответ сервера не по контракту.
	ErrInvalidResponse = errs.New(
		errs.Unavailable,
		"platform.invalid_response",
		"ответ сервера магазина не по контракту",
	)
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

type quoteJSON struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
	Cost      int64  `json:"cost"`
	MinDays   int    `json:"minDays"`
	MaxDays   int    `json:"maxDays"`
	ExpiresAt string `json:"expiresAt"`
}

// errorJSON — тело отказа сервера: {"ok":false,"message","status","code","items"}.
type errorJSON struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
	Items  []struct {
		ProductID ids.ProductID `json:"productId"`
		Price     *int64        `json:"price"`
	} `json:"items"`
}

type settingsJSON struct {
	FreeDeliveryFrom *int64                        `json:"freeDeliveryFrom"`
	Delivery         *participant.DeliverySettings `json:"delivery"`
	Subsidy          *struct {
		Enabled   bool   `json:"enabled"`
		Type      string `json:"type"`
		Value     int64  `json:"value"`
		MaxAmount int64  `json:"maxAmount"`
	} `json:"subsidy"`
}

// New — клиент API сервера.
func New(cfg Config) *Client {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	return &Client{
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		token:   cfg.Token,
		http:    &http.Client{Timeout: timeout, CheckRedirect: refuseRedirect},
	}
}

// CheckItems — цена и наличие позиций у магазина.
func (c *Client) CheckItems(
	ctx context.Context,
	tenantID ids.TenantID,
	request ports.ItemsCheckRequest,
) ([]ports.ItemCheck, error) {
	var answer struct {
		Items []itemCheckJSON `json:"items"`
	}
	body := map[string]any{"items": itemsJSON(request.Items)}
	if request.BuyerID != "" {
		body["buyerId"] = request.BuyerID
	}
	if err := c.call(ctx, http.MethodPost, tenantID, "/items/check", body, &answer); err != nil {
		return nil, err
	}
	checks := make([]ports.ItemCheck, 0, len(answer.Items))
	for _, item := range answer.Items {
		product, err := ids.ParseProductID(item.ProductID)
		if err != nil || item.Price < 0 || item.AvailableQuantity < 0 {
			return nil, ErrInvalidResponse.WithMessage("позиция ответа не по контракту: " + item.ProductID)
		}
		check := ports.ItemCheck{
			ProductID: product,
			Exists:    item.Available,
			Price:     money.FromKopecks(item.Price),
			Available: item.AvailableQuantity,
		}

		checks = append(checks, check)
	}

	return checks, nil
}

// QuoteDelivery — стоимость и срок доставки до пункта по договору магазина.
func (c *Client) QuoteDelivery(
	ctx context.Context,
	tenantID ids.TenantID,
	request ports.QuoteRequest,
) (delivery.Offered, error) {
	body := map[string]any{
		"carrier":         string(request.Carrier),
		"pickupPointCode": request.PickupPointCode,
		"city":            request.City,
		"items":           itemsJSON(request.Items),
	}

	if request.BuyerID != "" {
		body["buyerId"] = request.BuyerID
	}

	var answer quoteJSON
	if err := c.call(ctx, http.MethodPost, tenantID, "/delivery/quote", body, &answer); err != nil {
		return delivery.Offered{}, err
	}
	offered := delivery.Offered{
		Available: answer.Available,
		Reason:    answer.Reason,
		Cost:      money.FromKopecks(answer.Cost),
		MinDays:   answer.MinDays,
		MaxDays:   answer.MaxDays,
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

// MarketplaceSettings — обязательные настройки доставки магазина.
func (c *Client) MarketplaceSettings(ctx context.Context, tenantID ids.TenantID) (ports.ShopSettings, error) {
	var answer settingsJSON
	if err := c.call(ctx, http.MethodGet, tenantID, "/settings/marketplace", nil, &answer); err != nil {
		if errors.Is(err, ErrInvalidResponse) {
			return ports.ShopSettings{}, ports.ErrInvalidSettings.Wrap(err)
		}
		return ports.ShopSettings{}, err
	}
	settings := ports.ShopSettings{}
	if answer.FreeDeliveryFrom == nil || answer.Subsidy == nil || answer.Delivery == nil {
		return ports.ShopSettings{}, ports.ErrInvalidSettings
	}
	if answer.FreeDeliveryFrom != nil {
		if *answer.FreeDeliveryFrom < 0 {
			return ports.ShopSettings{}, ports.ErrInvalidSettings
		}
		settings.FreeDeliveryFrom = money.FromKopecks(*answer.FreeDeliveryFrom)
	}
	if answer.Delivery != nil {
		settings.Delivery = answer.Delivery.Clone()
	}
	if settings.Delivery.Validate() != nil {
		return ports.ShopSettings{}, ports.ErrInvalidSettings
	}

	if s := answer.Subsidy; s != nil {
		settings.Subsidy = participant.Subsidy{
			Enabled:   s.Enabled,
			Type:      participant.SubsidyType(s.Type),
			Value:     s.Value,
			MaxAmount: money.FromKopecks(s.MaxAmount),
		}
	}

	if err := settings.Subsidy.Validate(); err != nil {
		return ports.ShopSettings{}, ports.ErrInvalidSettings.Wrap(err)
	}

	if !settings.Subsidy.Enabled {
		return ports.ShopSettings{}, ports.ErrInvalidSettings
	}
	return settings, nil
}

func (c *Client) call(
	ctx context.Context,
	method string,
	tenantID ids.TenantID,
	path string,
	payload any,
	out any,
) error {
	var body io.Reader

	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("запрос к серверу: %w", err)
		}

		body = bytes.NewReader(encoded)
	}
	endpoint := c.baseURL + "/api/v1/internal/marketplace" + path
	if tenantID != "" {
		endpoint = c.baseURL + "/api/v1/internal/marketplace/tenants/" + url.PathEscape(string(tenantID)) + path
	}

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
		return refusal(method, path, tenantID, reply)
	}

	raw, err := io.ReadAll(io.LimitReader(reply.Body, (4<<20)+1))
	if err != nil || len(raw) > 4<<20 {
		return ErrInvalidResponse
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return ErrInvalidResponse.Wrap(err)
	}

	return nil
}

// refusal разбирает словарь отказов; неизвестный код сохраняется отдельно от терминального отказа.
func refusal(method, path string, tenant ids.TenantID, reply *http.Response) error {
	raw, err := io.ReadAll(io.LimitReader(reply.Body, (4<<20)+1))
	if err != nil || len(raw) > 4<<20 {
		return ErrInvalidResponse
	}
	detail := raw
	if len(detail) > 512 {
		detail = detail[:512]
	}
	message := fmt.Sprintf("%s %s: HTTP %d %s", method, path, reply.StatusCode, strings.TrimSpace(string(detail)))

	var body errorJSON
	if reply.StatusCode == http.StatusConflict && json.Unmarshal(raw, &body) == nil && body.Code == "PRICE_CHANGED" {
		changed := &ports.PriceChanged{}
		for _, item := range body.Items {
			_, err := ids.ParseProductID(string(item.ProductID))
			if err != nil || item.Price == nil || *item.Price < 0 {
				return ErrInvalidResponse
			}
			changed.Items = append(changed.Items, checkout.PriceChange{
				ProductID: item.ProductID,
				Price:     money.FromKopecks(*item.Price),
			})
		}
		if len(changed.Items) == 0 {
			return ErrInvalidResponse
		}
		return changed
	}
	if reply.StatusCode == http.StatusForbidden && json.Unmarshal(raw, &body) == nil {
		if body.Code == codeMarketplaceDisabled {
			return ports.ErrMarketplaceDisabled.WithMessage(message)
		}
	}

	if json.Unmarshal(raw, &body) == nil && body.Code != "" {
		switch {
		case reply.StatusCode == http.StatusConflict && (body.Code == "DELIVERY_UNAVAILABLE" ||
			body.Code == "DELIVERY_CHANGED" || body.Code == "OUT_OF_STOCK" || body.Code == "IDEMPOTENCY_CONFLICT"):
			return &ports.OrderRefusal{Code: body.Code, Reason: body.Reason}
		case reply.StatusCode == http.StatusNotFound && body.Code == "ORDER_NOT_FOUND":
			return &ports.OrderRefusal{Code: body.Code, Reason: body.Reason}
		}
		slog.Warn(
			"неизвестный отказ сервера магазина",
			slog.String("tenantId", string(tenant)),
			slog.String("code", body.Code),
			slog.Int("status", reply.StatusCode),
		)
		return &ports.OrderRefusal{Code: body.Code, Reason: body.Reason, Unexpected: true}
	}
	return ErrUnavailable.WithMessage(message)
}

func itemsJSON(items []ports.ItemRequest) []itemJSON {
	out := make([]itemJSON, 0, len(items))
	for _, item := range items {
		out = append(out, itemJSON{ProductID: string(item.ProductID), Quantity: item.Quantity})
	}

	return out
}

func refuseRedirect(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
