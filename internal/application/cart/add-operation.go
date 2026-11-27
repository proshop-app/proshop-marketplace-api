package cart

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	addKeyTTL = 24 * time.Hour
	// Аренда нужна для восстановления после падения реплики. Завершение защищено владельцем.
	addOperationLease = time.Minute
	addReleaseTimeout = 2 * time.Second
)

// NewAddKey выдаёт ключ для одной операции покупателя без записи в базу.
func (s *Service) NewAddKey(buyerID ids.UUID) string {
	expires := s.clock.Now().Add(addKeyTTL).UnixMilli()
	payload := strconv.FormatInt(expires, 10) + ":" + string(s.ids.NewUUID())
	return payload + ":" + s.signAddKey(buyerID, payload)
}

// AddOnce складывает количество один раз за срок действия ключа и возвращает текущую корзину.
func (s *Service) AddOnce(
	ctx context.Context,
	buyerID ids.UUID,
	offer ids.OfferID,
	quantity int,
	key string,
) (cart.State, error) {
	if quantity < 1 || quantity > cart.MaxQuantity {
		return cart.State{}, cart.ErrInvalidQuantity
	}
	now := s.clock.Now()
	expires, err := s.addKeyExpiry(buyerID, key, now)
	if err != nil {
		return cart.State{}, err
	}
	operation := cart.AddOperation{
		BuyerID:    buyerID,
		Key:        key,
		Offer:      offer,
		Quantity:   quantity,
		Owner:      s.ids.NewUUID(),
		LeaseUntil: now.Add(addOperationLease),
		ExpiresAt:  expires,
		Now:        now,
	}
	return s.add(ctx, buyerID, offer, quantity, operation)
}

func (s *Service) addKeyExpiry(buyerID ids.UUID, key string, now time.Time) (time.Time, error) {
	stamp, tail, ok := strings.Cut(key, ":")
	if !ok || len(stamp) > 13 {
		return time.Time{}, cart.ErrAddKey
	}
	nonce, signature, ok := strings.Cut(tail, ":")
	if !ok || len(signature) != base64.RawURLEncoding.EncodedLen(sha256.Size) {
		return time.Time{}, cart.ErrAddKey
	}
	if _, err := ids.ParseUUID(nonce); err != nil {
		return time.Time{}, cart.ErrAddKey
	}
	millis, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil || !hmac.Equal([]byte(signature), []byte(s.signAddKey(buyerID, stamp+":"+nonce))) {
		return time.Time{}, cart.ErrAddKey
	}
	expiry := time.UnixMilli(millis)
	if !expiry.After(now) {
		return time.Time{}, cart.ErrAddKey
	}
	return expiry, nil
}

func (s *Service) signAddKey(buyerID ids.UUID, payload string) string {
	mac := hmac.New(sha256.New, s.addKeySecret)
	_, _ = mac.Write([]byte("marketplace:cart:add:" + string(buyerID) + ":" + payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Service) releaseAdd(ctx context.Context, operation cart.AddOperation) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), addReleaseTimeout)
	defer cancel()

	_ = s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.Carts().ReleaseAdd(ctx, operation)
	})
}
