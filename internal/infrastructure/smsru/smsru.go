// Package smsru — проверка телефона звонком через sms.ru (callcheck): шлюз
// выдаёт номер, покупатель звонит на него со своего телефона, вызов
// сбрасывается. Запросы — формой, ответ — JSON с кодом в теле.
package smsru

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

const (
	// DefaultBaseURL — боевой адрес шлюза.
	DefaultBaseURL = "https://sms.ru"

	codeAccepted     = 100
	codeInvalidPhone = 202
)

// ErrRejected — шлюз отказал: нет денег на счёте, неверный ключ, сбой.
var ErrRejected = errs.New(errs.Unavailable, "smsru.rejected", "шлюз проверки звонком отказал")

// statuses — состояния проверки в ответе callcheck/status.
var statuses = map[string]buyer.CallStatus{
	"400": buyer.CallPending,
	"401": buyer.CallConfirmed,
	"402": buyer.CallExpired,
}

// Config — ключ и адрес шлюза.
type Config struct {
	BaseURL string
	APIID   string
	Timeout time.Duration
}

// Client — живой шлюз sms.ru.
type Client struct {
	baseURL string
	apiID   string
	http    *http.Client
}

// New — клиент шлюза.
func New(cfg Config) *Client {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	return &Client{baseURL: base, apiID: cfg.APIID, http: &http.Client{Timeout: timeout}}
}

type response struct {
	Status      string          `json:"status"`
	StatusCode  int             `json:"status_code"`
	StatusText  string          `json:"status_text"`
	CheckID     string          `json:"check_id"`
	CallPhone   string          `json:"call_phone"`
	CallPretty  string          `json:"call_phone_pretty"`
	CheckStatus json.RawMessage `json:"check_status"`
}

// Start заводит проверку. Частный и локальный адрес покупателя шлюзу не
// передаётся: на него sms.ru отвечает отказом, а без адреса выдаёт обычный номер.
func (c *Client) Start(ctx context.Context, phone buyer.Phone, ip string) (buyer.CallInstruction, error) {
	form := url.Values{"api_id": {c.apiID}, "phone": {string(phone)}, "json": {"1"}}
	if public := publicIP(ip); public != "" {
		form.Set("ip", public)
	}

	var answer response
	if err := c.post(ctx, "/callcheck/add", form, &answer); err != nil {
		return buyer.CallInstruction{}, err
	}

	switch {
	case answer.StatusCode == codeInvalidPhone:
		return buyer.CallInstruction{}, buyer.ErrInvalidPhone
	case answer.StatusCode != codeAccepted || answer.CheckID == "" || answer.CallPhone == "":
		return buyer.CallInstruction{}, ErrRejected.WithMessage(fmt.Sprintf("callcheck/add: %d %s", answer.StatusCode, answer.StatusText))
	}

	pretty := answer.CallPretty
	if pretty == "" {
		pretty = answer.CallPhone
	}

	return buyer.CallInstruction{ProviderRef: answer.CheckID, CallPhone: answer.CallPhone, CallPhonePretty: pretty}, nil
}

// Status — состояние проверки у шлюза.
func (c *Client) Status(ctx context.Context, providerRef string) (buyer.CallStatus, error) {
	var answer response
	if err := c.post(ctx, "/callcheck/status", url.Values{"api_id": {c.apiID}, "check_id": {providerRef}, "json": {"1"}}, &answer); err != nil {
		return "", err
	}
	if answer.StatusCode != codeAccepted {
		return "", ErrRejected.WithMessage(fmt.Sprintf("callcheck/status: %d %s", answer.StatusCode, answer.StatusText))
	}

	// шлюз отдаёт код состояния то строкой, то числом
	code := strings.Trim(string(answer.CheckStatus), `"`)

	status, ok := statuses[code]
	if !ok {
		return buyer.CallPending, nil
	}

	return status, nil
}

// RegisterCallback заводит у шлюза адрес оповещений о проверках.
func (c *Client) RegisterCallback(ctx context.Context, callbackURL string) error {
	var answer response
	if err := c.post(ctx, "/callback/add", url.Values{"api_id": {c.apiID}, "url": {callbackURL}, "json": {"1"}}, &answer); err != nil {
		return err
	}
	if answer.StatusCode != codeAccepted {
		return ErrRejected.WithMessage(fmt.Sprintf("callback/add: %d %s", answer.StatusCode, answer.StatusText))
	}

	return nil
}

func (c *Client) post(ctx context.Context, path string, form url.Values, out *response) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("запрос к sms.ru: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	reply, err := c.http.Do(request)
	if err != nil {
		return ErrRejected.Wrap(err)
	}
	defer func() { _ = reply.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(reply.Body, 1<<20))
	if err != nil {
		return ErrRejected.Wrap(err)
	}
	if reply.StatusCode != http.StatusOK {
		return ErrRejected.WithMessage(fmt.Sprintf("%s: HTTP %d", path, reply.StatusCode))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return ErrRejected.Wrap(err)
	}

	return nil
}

// publicIP — адрес покупателя, если он публичный.
func publicIP(raw string) string {
	ip := net.ParseIP(strings.TrimSpace(raw))
	if ip == nil || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return ""
	}

	return ip.String()
}
