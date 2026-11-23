// Package platformapi — клиент внутреннего API сервера установки для
// маркетплейса (контракт PR-M1, docs/server-contract.md): служебный токен в
// x-internal-token, магазин в пути, суммы — целые копейки.
package platformapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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
	// HeaderDeadline — абсолютный срок вызова, Unix миллисекунды; сервер расходует оставшееся время.
	HeaderDeadline = "x-marketplace-deadline-ms"

	// codeMarketplaceDisabled — код отказа сервера для магазина, который не участвует в маркетплейсе.
	codeMarketplaceDisabled = "MARKETPLACE_DISABLED"
	// codePromoInvalid — сервер отклонил промокод заказа.
	codePromoInvalid = "MARKETPLACE_PROMO_CODE_INVALID"
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
	Discounts         []discountJSON `json:"discounts"`
	ProductID         string         `json:"productId"`
	Available         bool           `json:"available"`
	Price             int64          `json:"price"`
	AvailableQuantity int            `json:"availableQuantity"`
}

type quoteJSON struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
	Cost      int64  `json:"cost"`
	MinDays   int    `json:"minDays"`
	MaxDays   int    `json:"maxDays"`
	ExpiresAt string `json:"expiresAt"`
}

// errorJSON — тело отказа сервера: {"ok":false,"message","status","code","items","data"}.
type errorJSON struct {
	Data *struct {
		PromoCode              *promoCodeJSON `json:"promoCode"`
		DispatchReviewRevision int            `json:"dispatchReviewRevision"`
	} `json:"data"`
	Code   string `json:"code"`
	Reason string `json:"reason"`
	Items  []struct {
		ProductID ids.ProductID `json:"productId"`
		Price     *int64        `json:"price"`
	} `json:"items"`
}

type settingsProfileJSON struct {
	Name string  `json:"name"`
	Logo *string `json:"logo"`
	Mark *string `json:"mark"`
}

type settingsJSON struct {
	Profile  *settingsProfileJSON          `json:"profile"`
	Card     *participant.CardReadiness    `json:"card"`
	Returns  *participant.Returns          `json:"returns"`
	Delivery *participant.DeliverySettings `json:"delivery"`
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

	if request.DispatchReview != nil {
		body["dispatchReview"] = request.DispatchReview
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
	if answer.Profile == nil || answer.Card == nil {
		return ports.ShopSettings{}, ports.ErrInvalidSettings
	}
	settings := ports.ShopSettings{
		Profile: participant.Profile{Name: answer.Profile.Name, Logo: answer.Profile.Logo, Mark: answer.Profile.Mark},
		Card:    *answer.Card,
		Returns: answer.Returns,
	}
	if answer.Delivery != nil {
		if err := answer.Delivery.Validate(); err != nil {
			return ports.ShopSettings{}, ports.ErrInvalidSettings.Wrap(err)
		}
		settings.Delivery = answer.Delivery.Clone()
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
	return c.callWithRefusal(ctx, method, tenantID, path, payload, out, refusal)
}

func (c *Client) callWithRefusal(
	ctx context.Context,
	method string,
	tenantID ids.TenantID,
	path string,
	payload any,
	out any,
	parseRefusal func(string, string, ids.TenantID, *http.Response) error,
) error {
	request, err := c.newRequest(ctx, method, tenantID, path, payload)
	if err != nil {
		return err
	}

	reply, err := c.http.Do(request)
	if err != nil {
		return transportFailure(err)
	}
	defer func() { _ = reply.Body.Close() }()

	if reply.StatusCode != http.StatusOK {
		return responseRefusal(method, path, tenantID, reply, parseRefusal)
	}
	return decodeResponse(reply.Body, out)
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
	if reply.StatusCode == http.StatusConflict && json.Unmarshal(raw, &body) == nil &&
		body.Code == codePromoInvalid {
		if body.Data != nil && body.Data.PromoCode != nil && body.Data.PromoCode.Message != nil {
			return checkout.ErrPromoInvalid.WithMessage(*body.Data.PromoCode.Message)
		}
		return checkout.ErrPromoInvalid
	}
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

	if reply.StatusCode == http.StatusNotFound && strings.HasSuffix(path, "/stub-payment") {
		if json.Unmarshal(raw, &body) != nil || body.Code != "ORDER_NOT_FOUND" {
			return ErrUnavailable.WithMessage(message)
		}
	}
	if json.Unmarshal(raw, &body) == nil && body.Code != "" {
		switch {
		case reply.StatusCode == http.StatusBadRequest && body.Code == "MARKETPLACE_PAYMENT_DUE_INVALID":
			return &ports.OrderRefusal{Code: body.Code}
		case reply.StatusCode == http.StatusConflict &&
			(body.Code == "ORDER_PAYMENT_EXPIRED" || body.Code == "ORDER_NOT_AWAITING_PAYMENT"):
			return &ports.OrderRefusal{Code: body.Code}
		case reply.StatusCode == http.StatusServiceUnavailable && body.Code == "MARKETPLACE_TEMPORARY_FAILURE":
			return ErrUnavailable.Wrap(&ports.OrderRefusal{Code: body.Code})
		case reply.StatusCode == http.StatusConflict && strings.HasSuffix(path, "/stub-payment") &&
			(body.Code == "MARKETPLACE_STUB_PAYMENT_AMOUNT" || body.Code == "MARKETPLACE_STUB_PAYMENT_STATE"):
			return &ports.OrderRefusal{Code: body.Code, Reason: body.Reason}
		case reply.StatusCode == http.StatusConflict && body.Code == "MARKETPLACE_PICKUP_CATALOG_CHANGED":
			return &ports.PickupCatalogFailure{Cause: &ports.OrderRefusal{Code: body.Code}}
		case reply.StatusCode == http.StatusConflict && body.Code == checkout.DispatchOriginChanged:
			if body.Data == nil || body.Data.DispatchReviewRevision <= 0 {
				return ErrInvalidResponse
			}
			return &ports.OrderRefusal{
				Code:                   body.Code,
				DispatchReviewRevision: body.Data.DispatchReviewRevision,
			}
		case reply.StatusCode == http.StatusConflict && (body.Code == "DELIVERY_UNAVAILABLE" ||
			body.Code == "DELIVERY_CHANGED" ||
			body.Code == "OUT_OF_STOCK" ||
			body.Code == "IDEMPOTENCY_CONFLICT" || body.Code == "ORDER_CHECKOUT_CANCELLED"):
			return &ports.OrderRefusal{Code: body.Code, Reason: body.Reason}
		case reply.StatusCode == http.StatusNotFound && body.Code == "ORDER_NOT_FOUND":
			return &ports.OrderRefusal{Code: body.Code, Reason: body.Reason}
		}
		if tenant != "" {
			slog.Warn(
				"неизвестный отказ сервера магазина",
				slog.String("tenantId", string(tenant)),
				slog.String("code", body.Code),
				slog.Int("status", reply.StatusCode),
			)
		}
		failure := &ports.OrderRefusal{Code: body.Code, Reason: body.Reason, Unexpected: true}
		if reply.StatusCode >= http.StatusInternalServerError {
			return ErrUnavailable.Wrap(failure)
		}
		return failure
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
