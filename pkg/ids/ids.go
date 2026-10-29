// Package ids — идентификаторы платформы. Формат совпадает с источником
// (сервер установки): технические ключи без пробелов и экранирования.
package ids

import (
	"regexp"
	"strings"

	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

// ErrInvalid — строка не является идентификатором.
var ErrInvalid = errs.New(errs.Invalid, "ids.invalid", "некорректный идентификатор")

var pattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// offerSeparator не входит в алфавит идентификаторов, поэтому разбор однозначен.
const offerSeparator = "~"

// TenantID — магазин платформы.
type TenantID string

// ProductID — товар в каталоге магазина.
type ProductID string

// OfferID — предложение: товар конкретного магазина. Отдельного offerId
// платформа не вводит, ключ — пара магазина и товара.
type OfferID struct {
	Tenant  TenantID
	Product ProductID
}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// UUID — идентификатор сущности маркетплейса: покупателя, проверки входа,
// корзины, покупки. Строчные шестнадцатеричные цифры с дефисами.
type UUID string

// ParseTenantID проверяет формат идентификатора магазина.
func ParseTenantID(value string) (TenantID, error) {
	if err := validate("магазина", value); err != nil {
		return "", err
	}

	return TenantID(value), nil
}

// ParseProductID проверяет формат идентификатора товара.
func ParseProductID(value string) (ProductID, error) {
	if err := validate("товара", value); err != nil {
		return "", err
	}

	return ProductID(value), nil
}

func validate(subject, value string) error {
	if !pattern.MatchString(value) {
		return ErrInvalid.WithMessage("некорректный идентификатор " + subject + ": " + strings.TrimSpace(value))
	}

	return nil
}

// String — запись для адресов и индекса: "<tenantId>~<productId>".
func (o OfferID) String() string {
	return string(o.Tenant) + offerSeparator + string(o.Product)
}

// ParseOfferID разбирает запись "<tenantId>~<productId>".
func ParseOfferID(value string) (OfferID, error) {
	tenant, product, found := strings.Cut(value, offerSeparator)
	if !found {
		return OfferID{}, ErrInvalid.WithMessage("некорректный идентификатор предложения")
	}

	tenantID, err := ParseTenantID(tenant)
	if err != nil {
		return OfferID{}, err
	}

	productID, err := ParseProductID(product)
	if err != nil {
		return OfferID{}, err
	}

	return OfferID{Tenant: tenantID, Product: productID}, nil
}

// ParseUUID проверяет запись UUID.
func ParseUUID(value string) (UUID, error) {
	if !uuidPattern.MatchString(value) {
		return "", ErrInvalid.WithMessage("некорректный идентификатор")
	}

	return UUID(value), nil
}
