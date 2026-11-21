// Package searchfeedback передаёт поиску поведенческие события площадки.
package searchfeedback

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/netip"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Виды органических событий и пределы тела публичного запроса.
const (
	Click          = "click"
	Impression     = "impression"
	MaxImpressions = 48
	MaxBodyBytes   = 16 << 10
)

const (
	receiptHexLength = 64
	ratePerMinute    = 120
	limitTimeout     = 25 * time.Millisecond
	feedbackTimeout  = 500 * time.Millisecond
)

// ErrInvalid означает нарушение формы события до вызова поиска.
var ErrInvalid = errs.New(errs.Invalid, "search.invalid_feedback", "некорректное событие поиска")

// BuyerKeys вычисляет одинаковый псевдоним для выдачи, клика и оплаты.
type BuyerKeys struct {
	key []byte
}

// Feedback ограничивает частоту публичных событий перед вызовом поиска.
type Feedback struct {
	sink    ports.SearchFeedbackSink
	limiter ports.RateLimiter
	keys    *BuyerKeys
}

// NewBuyerKeys закрепляет общий ключ аналитики процесса.
func NewBuyerKeys(key string) *BuyerKeys {
	return &BuyerKeys{key: []byte(key)}
}

// Key вычисляет псевдоним в области площадки. Гость остаётся без псевдонима.
func (k *BuyerKeys) Key(buyerID ids.UUID) string {
	if buyerID == "" {
		return ""
	}
	return k.digest("marketplace", string(buyerID))
}

func (k *BuyerKeys) digest(scope, value string) string {
	mac := hmac.New(sha256.New, k.key)
	_, _ = mac.Write([]byte(scope + "\x00" + value))
	return hex.EncodeToString(mac.Sum(nil))
}

// NewFeedback подключает общий лимитер и приёмник поиска.
func NewFeedback(sink ports.SearchFeedbackSink, limiter ports.RateLimiter, keys *BuyerKeys) *Feedback {
	return &Feedback{sink: sink, limiter: limiter, keys: keys}
}

// Allow расходует одну попытку на POST, включая пачку показов.
func (f *Feedback) Allow(ctx context.Context, address string) bool {
	ctx, cancel := context.WithTimeout(ctx, limitTimeout)
	defer cancel()
	networkKey := f.keys.digest("marketplace-feedback-network", feedbackNetwork(address))
	allowed, _, err := f.limiter.Allow(ctx, "search:feedback:"+networkKey, ratePerMinute, time.Minute)
	return err == nil && allowed
}

func feedbackNetwork(address string) string {
	ip, err := netip.ParseAddr(address)
	if err != nil {
		return ""
	}
	ip = ip.Unmap().WithZone("")
	bits := ip.BitLen()
	if ip.Is6() {
		bits = 64
	}
	return netip.PrefixFrom(ip, bits).Masked().String()
}

// Record сообщает, принял ли поиск хотя бы одно предложение события.
func (f *Feedback) Record(ctx context.Context, event ports.SearchFeedback) bool {
	ctx, cancel := context.WithTimeout(ctx, feedbackTimeout)
	defer cancel()
	accepted, err := f.sink.Feedback(ctx, event)
	return err == nil && accepted
}

// ValidReceipt проверяет формат квитанции без разбора номера раздела.
func ValidReceipt(value string) bool {
	if len(value) != receiptHexLength {
		return false
	}
	for i := range len(value) {
		c := value[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
