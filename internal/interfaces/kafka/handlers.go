package kafka

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// projectionVersion — версия контракта проекций сервера.
const projectionVersion = 1

type productProjection struct {
	Version   int              `json:"version"`
	TenantID  string           `json:"tenantId"`
	ProductID string           `json:"productId"`
	Revision  int64            `json:"revision"`
	Operation string           `json:"operation"`
	Product   *productSnapshot `json:"product"`
}

type productSnapshot struct {
	Marketplace       *taxonomy.Card  `json:"marketplace"`
	ID                string          `json:"id"`
	Name              string          `json:"name"`
	Description       string          `json:"description"`
	Image             *string         `json:"image"`
	URL               string          `json:"url"`
	SKU               string          `json:"sku"`
	Price             *json.Number    `json:"price"`
	PromoPrice        *json.Number    `json:"promoPrice"`
	AvailableQuantity *json.Number    `json:"availableQuantity"`
	Categories        []snapshotGroup `json:"categories"`
	Tags              []string        `json:"tags"`
	VariantsID        *string         `json:"variantsId"`
	Conditions        *snapshotFlags  `json:"conditions"`
}

type snapshotGroup struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

type snapshotFlags struct {
	IsVisible     *bool `json:"isVisible"`
	IsOnlyVariant *bool `json:"isOnlyVariant"`
}

type tagProjection struct {
	Version   int    `json:"version"`
	TenantID  string `json:"tenantId"`
	TagID     string `json:"tagId"`
	Revision  int64  `json:"revision"`
	Operation string `json:"operation"`
	Tag       *struct {
		ID    string `json:"id"`
		Value string `json:"value"`
	} `json:"tag"`
}

type stockItem struct {
	ProductID string       `json:"productId"`
	Quantity  *json.Number `json:"quantity"`
	Reserved  *json.Number `json:"reserved"`
	Price     *json.Number `json:"price"`
}

type priceChange struct {
	ID    string       `json:"id"`
	Price *json.Number `json:"price"`
}

type tenantRecord struct {
	TenantID string          `json:"tenantId"`
	Slug     string          `json:"slug"`
	Status   string          `json:"status"`
	Cell     string          `json:"cell"`
	Plan     json.RawMessage `json:"plan"`
	Hosts    json.RawMessage `json:"hosts"`
	At       string          `json:"at"`
}

// productProjected — снимок товара: магазин в ключе и теле, заголовок — если есть.
func productProjected(ctx context.Context, projections Projections, envelope Envelope) error {
	var event productProjection
	if err := decode(envelope.Message, &event); err != nil {
		return err
	}

	keyTenant, keyProduct, err := projectionKey(envelope.Message)
	if err != nil {
		return err
	}
	if event.TenantID == "" || keyTenant == "" {
		return ErrTenantMissing
	}

	tenant, err := resolveTenant(keyTenant, event.TenantID, tenantHeader(envelope.Message))
	if err != nil {
		return err
	}
	if event.Version != projectionVersion || keyProduct != event.ProductID {
		return ErrInvalidPayload.WithMessage("версия контракта или товар ключа не совпадают с телом")
	}

	product, err := ids.ParseProductID(event.ProductID)
	if err != nil {
		return ErrInvalidPayload.Wrap(err)
	}

	at, err := eventTime(envelope.Message)
	if err != nil {
		return err
	}

	snapshot := catalog.Snapshot{TenantID: tenant, ProductID: product, Revision: event.Revision, At: at}

	switch event.Operation {
	case "delete":
		if event.Product != nil {
			return ErrInvalidPayload.WithMessage("удаление со снимком товара")
		}
		snapshot.Deleted = true
	case "upsert":
		if event.Product == nil || event.Product.ID != event.ProductID {
			return ErrInvalidPayload.WithMessage("снимок товара не совпадает с конвертом")
		}
		if err := fillSnapshot(&snapshot, event.Product); err != nil {
			return err
		}
	default:
		return ErrInvalidPayload.WithMessage("неизвестная операция проекции: " + event.Operation)
	}

	return projections.ApplyProduct(ctx, appcatalog.ProductEvent{EventID: envelope.EventID, Snapshot: snapshot})
}

func fillSnapshot(snapshot *catalog.Snapshot, product *productSnapshot) error {
	if product.Price == nil {
		return ErrInvalidPayload.WithMessage("у товара нет цены")
	}

	price, err := rubles(*product.Price)
	if err != nil {
		return err
	}

	snapshot.Price = price

	if product.PromoPrice != nil {
		promo, err := rubles(*product.PromoPrice)
		if err != nil {
			return err
		}

		snapshot.PromoPrice = &promo
	}

	if snapshot.Available, err = quantity(product.AvailableQuantity); err != nil {
		return err
	}

	card := catalog.Card{
		Marketplace: product.Marketplace,
		Name:        product.Name,
		Description: product.Description,
		URL:         product.URL,
		SKU:         product.SKU,
		Tags:        product.Tags,
		Visible:     true,
	}
	if product.Image != nil {
		card.Image = *product.Image
	}
	if product.VariantsID != nil {
		card.VariantsID = *product.VariantsID
	}
	if flags := product.Conditions; flags != nil {
		if flags.IsVisible != nil {
			card.Visible = *flags.IsVisible
		}
		if flags.IsOnlyVariant != nil {
			card.OnlyVariant = *flags.IsOnlyVariant
		}
	}

	for _, group := range product.Categories {
		name := strings.TrimSpace(group.Name)
		if name == "" {
			name = strings.TrimSpace(group.Title)
		}

		card.Categories = append(card.Categories, catalog.Category{ID: group.ID, Name: name, URL: group.URL})
	}

	snapshot.Card = card

	return nil
}

// tagProjected — снимок тега: магазин в ключе и теле.
func tagProjected(ctx context.Context, projections Projections, envelope Envelope) error {
	var event tagProjection
	if err := decode(envelope.Message, &event); err != nil {
		return err
	}

	keyTenant, keyTag, err := projectionKey(envelope.Message)
	if err != nil {
		return err
	}
	if event.TenantID == "" || keyTenant == "" {
		return ErrTenantMissing
	}

	tenant, err := resolveTenant(keyTenant, event.TenantID, tenantHeader(envelope.Message))
	if err != nil {
		return err
	}
	if event.Version != projectionVersion || keyTag != event.TagID {
		return ErrInvalidPayload.WithMessage("версия контракта или тег ключа не совпадают с телом")
	}

	tag, err := ids.ParseTagID(event.TagID)
	if err != nil {
		return ErrInvalidPayload.Wrap(err)
	}

	snapshot := catalog.TagSnapshot{TenantID: tenant, TagID: tag, Revision: event.Revision}

	switch event.Operation {
	case "delete":
		if event.Tag != nil {
			return ErrInvalidPayload.WithMessage("удаление со снимком тега")
		}
		snapshot.Deleted = true
	case "upsert":
		if event.Tag == nil || event.Tag.ID != event.TagID {
			return ErrInvalidPayload.WithMessage("снимок тега не совпадает с конвертом")
		}
		snapshot.Value = event.Tag.Value
	default:
		return ErrInvalidPayload.WithMessage("неизвестная операция проекции: " + event.Operation)
	}

	return projections.ApplyTag(ctx, appcatalog.TagEvent{EventID: envelope.EventID, Snapshot: snapshot})
}

// stockItemUpdated — позиция склада: магазин только в заголовке.
func stockItemUpdated(ctx context.Context, projections Projections, envelope Envelope) error {
	tenant, err := resolveTenant(tenantHeader(envelope.Message))
	if err != nil {
		return err
	}

	var item stockItem
	if err := decode(envelope.Message, &item); err != nil {
		return err
	}

	product, err := ids.ParseProductID(item.ProductID)
	if err != nil {
		return ErrInvalidPayload.Wrap(err)
	}

	physical, err := quantity(item.Quantity)
	if err != nil {
		return err
	}

	reserved, err := quantity(item.Reserved)
	if err != nil {
		return err
	}

	at, err := eventTime(envelope.Message)
	if err != nil {
		return err
	}

	event := appcatalog.StockEvent{
		EventID:   envelope.EventID,
		TenantID:  tenant,
		ProductID: product,
		Stock:     catalog.StockSignal{Available: max(physical-reserved, 0), At: at},
	}

	if item.Price != nil {
		price, err := rubles(*item.Price)
		if err != nil {
			return err
		}

		event.Price = &catalog.PriceSignal{Price: price, At: at}
	}

	return projections.ApplyStock(ctx, event)
}

// priceUpdated — новый прайс товара: магазин только в заголовке.
func priceUpdated(ctx context.Context, projections Projections, envelope Envelope) error {
	tenant, err := resolveTenant(tenantHeader(envelope.Message))
	if err != nil {
		return err
	}

	var change priceChange
	if err := decode(envelope.Message, &change); err != nil {
		return err
	}

	product, err := ids.ParseProductID(change.ID)
	if err != nil {
		return ErrInvalidPayload.Wrap(err)
	}
	if change.Price == nil {
		return ErrInvalidPayload.WithMessage("в событии нет цены")
	}

	price, err := rubles(*change.Price)
	if err != nil {
		return err
	}

	at, err := eventTime(envelope.Message)
	if err != nil {
		return err
	}

	return projections.ApplyPrice(ctx, appcatalog.PriceEvent{
		EventID:   envelope.EventID,
		TenantID:  tenant,
		ProductID: product,
		Price:     catalog.PriceSignal{Price: price, At: at},
	})
}

// tenantEvent — событие реестра: магазин в теле, заголовок — если есть.
// Событие удаления переводит запись в конечный статус независимо от тела.
func tenantEvent(ctx context.Context, registry Registry, envelope Envelope) error {
	var record tenantRecord
	if err := decode(envelope.Message, &record); err != nil {
		return err
	}
	if record.TenantID == "" {
		return ErrTenantMissing
	}

	tenant, err := resolveTenant(record.TenantID, tenantHeader(envelope.Message))
	if err != nil {
		return err
	}

	at, err := time.Parse(time.RFC3339Nano, record.At)
	if err != nil {
		return ErrInvalidPayload.WithMessage("время события реестра не в RFC 3339: " + record.At)
	}

	status := participant.TenantStatus(record.Status)
	if envelope.Topic == TopicTenantDeleted {
		status = participant.TenantDeleted
	}

	return registry.Apply(ctx, appparticipant.RegistryEvent{EventID: envelope.EventID, Event: participant.RegistryEvent{
		TenantID: tenant,
		Slug:     record.Slug,
		Status:   status,
		Cell:     record.Cell,
		Plan:     nullable(record.Plan),
		Hosts:    nullable(record.Hosts),
		At:       at.UTC().Truncate(time.Microsecond),
	}})
}

func nullable(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}

	return raw
}
