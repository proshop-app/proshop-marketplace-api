package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

const (
	checkoutColumns = `
		id, buyer_id, idempotency_key, status, carrier, pickup_point_id, delivery, goods_total, delivery_total,
		total, version, created_at, updated_at, confirmed_at, order_context`

	// checkoutReadColumns — колонки чтения: номер выдаёт база при вставке, в запись он не входит.
	checkoutReadColumns = checkoutColumns + `, number`

	// selectCheckout дополняется условием отбора.
	selectCheckout = `
		SELECT ` + checkoutReadColumns + `
		FROM checkouts
		WHERE `

	insertCheckout = `
		INSERT INTO checkouts (` + checkoutColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
		RETURNING number`

	groupColumns = `
		tenant_id, external_checkout_id, lines, goods, quote_status, quote_reason, delivery_cost,
		min_days, max_days, quote_expires_at, order_progress, tracking`

	// lockCheckout — условие чтения покупки под запись: строка заблокирована до конца транзакции.
	lockCheckout = `id = $1 FOR NO KEY UPDATE`
)

// Checkouts — покупки в PostgreSQL.
type Checkouts struct {
	db *gorm.DB
}

// lineJSON — позиция снимка в колонке jsonb; цена — копейки.
type lineJSON struct {
	Discounts []cart.Discount `json:"discounts,omitempty"`
	ProductID string          `json:"productId"`
	Name      string          `json:"name"`
	Quantity  int             `json:"quantity"`
	Price     int64           `json:"price"`
}

// selectionJSON — выбранная доставка в колонке jsonb.
type selectionJSON struct {
	City       string `json:"city"`
	Address    string `json:"address"`
	RegionCode string `json:"regionCode"`
}

// checkoutRow — строка покупки; доставка пуста, пока её не выбрали.
type checkoutRow struct {
	state               checkout.State
	id, buyerID, status string
	carrier, pointID    *string
	selection           []byte
	goods, deliv, total int64
	createdAt           pgtype.Timestamptz
	updatedAt           pgtype.Timestamptz
	confirmedAt         pgtype.Timestamptz
	orderContext        []byte
	stuckAt             *time.Time
	invalidStuckAt      bool
}

// groupRow — строка группы; поля расчёта пусты, пока доставку не считали.
type groupRow struct {
	tracking             []byte
	tenantID, externalID *string
	lines                []byte
	goods                *int64
	quoteStatus, reason  *string
	cost                 *int64
	minDays, maxDays     *int
	expiresAt            pgtype.Timestamptz
	orderProgress        []byte
}

// Get — покупка.
func (r *Checkouts) Get(ctx context.Context, id ids.UUID) (*checkout.Checkout, error) {
	return r.one(ctx, `id = $1`, string(id))
}

// GetForBuyer проверяет владельца до разбора покупки и групп.
func (r *Checkouts) GetForBuyer(ctx context.Context, buyerID, id ids.UUID) (*checkout.Checkout, error) {
	return r.one(ctx, `id = $1 AND buyer_id = $2`, string(id), string(buyerID))
}

// GetForUpdate — покупка под запись: параллельная запись той же покупки ждёт конца транзакции,
// а не проигрывает условное обновление версии.
func (r *Checkouts) GetForUpdate(ctx context.Context, id ids.UUID) (*checkout.Checkout, error) {
	return r.one(ctx, lockCheckout, string(id))
}

// ByKey — покупка покупателя по ключу идемпотентности.
func (r *Checkouts) ByKey(ctx context.Context, buyerID ids.UUID, key string) (*checkout.Checkout, error) {
	return r.one(ctx, `buyer_id = $1 AND idempotency_key = $2`, string(buyerID), key)
}

// Add сохраняет новую покупку с группами.
func (r *Checkouts) Add(ctx context.Context, c *checkout.Checkout) error {
	s := c.State()

	carrier, point, selection, err := selectionColumns(s.Delivery)
	if err != nil {
		return err
	}

	var number int64

	err = rawSQL(
		ctx,
		r.db,
		insertCheckout,
		string(s.ID),
		string(s.BuyerID),
		s.IdempotencyKey,
		string(s.Status),
		carrier,
		point,
		selection,
		s.GoodsTotal.Kopecks(),
		s.DeliveryTotal.Kopecks(),
		s.Total.Kopecks(),
		s.Version,
		s.CreatedAt,
		s.UpdatedAt,
		nullTime(s.ConfirmedAt),
		encodeOrderContext(s),
	).Row().Scan(&number)
	if isUnique(err, "checkouts_buyer_key_key") {
		return checkout.ErrDuplicateKey
	}
	if err != nil {
		return fmt.Errorf("новая покупка: %w", err)
	}

	if err := r.writeGroups(ctx, insertCheckoutGroups, []checkout.State{s}); err != nil {
		return err
	}

	c.AssignNumber(number)
	c.MarkSaved()

	return nil
}

// Update сохраняет условно по исходным статусу и версии; переписываются только изменившиеся группы.
func (r *Checkouts) Update(ctx context.Context, c *checkout.Checkout) error {
	return r.UpdateMany(ctx, []*checkout.Checkout{c})
}

func (r *Checkouts) one(ctx context.Context, where string, args ...any) (*checkout.Checkout, error) {
	var row checkoutRow

	err := rawSQL(ctx, r.db, selectCheckout+where, args...).Row().Scan(row.targets()...)
	if notFound(err) {
		return nil, checkout.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("покупка: %w", err)
	}

	entry, state := row.listEntry()
	states := []checkout.State{state}
	loaded, err := r.loadGroups(ctx, states)
	if err != nil {
		return nil, err
	}
	entry.Failures = loaded.failures(entry, states[0])
	if failure := entry.ReadError(); failure != nil {
		if errors.Is(failure, checkout.ErrConflict) {
			return nil, checkout.ErrConflict
		}
		return nil, failure
	}
	return checkout.Restore(states[0]), nil
}

func (r *checkoutRow) targets() []any {
	return []any{
		&r.id,
		&r.buyerID,
		&r.state.IdempotencyKey,
		&r.status,
		&r.carrier,
		&r.pointID,
		&r.selection,
		&r.goods,
		&r.deliv,
		&r.total,
		&r.state.Version,
		&r.createdAt,
		&r.updatedAt,
		&r.confirmedAt,
		&r.orderContext,
		&r.state.Number,
	}
}

func (r *checkoutRow) checkout() (checkout.State, error) {
	state := r.state
	state.ID = ids.UUID(r.id)
	state.BuyerID = ids.UUID(r.buyerID)
	if r.createdAt.InfinityModifier != pgtype.Finite || r.updatedAt.InfinityModifier != pgtype.Finite ||
		r.confirmedAt.InfinityModifier != pgtype.Finite {
		return state, fmt.Errorf("бесконечная дата покупки")
	}
	state.CreatedAt, state.UpdatedAt, state.ConfirmedAt = r.createdAt.Time, r.updatedAt.Time, r.confirmedAt.Time
	var meta checkoutOrderContext
	if err := json.Unmarshal(r.orderContext, &meta); err != nil {
		return state, err
	}
	state.Contact, state.CartConsumed = meta.Contact, meta.CartConsumed
	state.PaymentStarted = meta.PaymentStarted
	state.PaymentAttempt = meta.PaymentAttempt
	state.Status = checkout.Status(r.status)
	state.GoodsTotal = money.FromKopecks(r.goods)
	state.DeliveryTotal = money.FromKopecks(r.deliv)
	state.Total = money.FromKopecks(r.total)

	if r.carrier == nil {
		return state, nil
	}

	selection, err := decodeSelection(*r.carrier, r.pointID, r.selection)
	if err != nil {
		return state, err
	}
	state.Delivery = &selection

	return state, nil
}

func decodeSelection(carrier string, pointID *string, raw []byte) (checkout.Selection, error) {
	if pointID == nil || *pointID == "" {
		return checkout.Selection{}, fmt.Errorf("не указан пункт выдачи")
	}
	var doc *selectionJSON
	if err := json.Unmarshal(raw, &doc); err != nil {
		return checkout.Selection{}, fmt.Errorf("доставка в пункт %s: %w", *pointID, err)
	}
	if doc == nil {
		return checkout.Selection{}, fmt.Errorf("нет снимка доставки в пункт %s", *pointID)
	}
	return checkout.Selection{
		Carrier:         delivery.Carrier(carrier),
		PickupPointCode: *pointID,
		City:            doc.City,
		Address:         doc.Address,
		RegionCode:      doc.RegionCode,
	}, nil
}

func (r *groupRow) targets() []any {
	return []any{
		&r.tenantID,
		&r.externalID,
		&r.lines,
		&r.goods,
		&r.quoteStatus,
		&r.reason,
		&r.cost,
		&r.minDays,
		&r.maxDays,
		&r.expiresAt,
		&r.orderProgress,
		&r.tracking,
	}
}

func (r *groupRow) checkoutGroup() (checkout.Group, error) {
	group := checkout.Group{ExternalCheckoutID: *r.externalID}
	if err := json.Unmarshal(r.orderProgress, &group); err != nil {
		return checkout.Group{}, err
	}
	if len(r.tracking) > 0 {
		// Tracking исключён из order_progress, поэтому читается отдельно.
		var tracking *checkout.Tracking
		if err := json.Unmarshal(r.tracking, &tracking); err != nil {
			return checkout.Group{}, err
		}
		group.Tracking = tracking
	}
	group.TenantID = ids.TenantID(*r.tenantID)
	group.Goods = money.FromKopecks(*r.goods)

	var docs []lineJSON
	if err := json.Unmarshal(r.lines, &docs); err != nil {
		return checkout.Group{}, err
	}

	for _, doc := range docs {
		line := checkout.Line{
			Offer:     ids.OfferID{Tenant: group.TenantID, Product: ids.ProductID(doc.ProductID)},
			Name:      doc.Name,
			Quantity:  doc.Quantity,
			Price:     money.FromKopecks(doc.Price),
			Discounts: doc.Discounts,
		}

		group.Lines = append(group.Lines, line)
	}

	if r.quoteStatus != nil && (!r.expiresAt.Valid || r.expiresAt.InfinityModifier != pgtype.Finite ||
		(r.cost != nil && (r.minDays == nil || r.maxDays == nil))) {
		return checkout.Group{}, fmt.Errorf("неполный расчёт доставки")
	}
	group.Quote = r.quote()

	return group, nil
}

func (r *groupRow) quote() *delivery.Quote {
	if r.quoteStatus == nil {
		return nil
	}

	quote := delivery.Quote{
		Status:    delivery.QuoteStatus(*r.quoteStatus),
		ExpiresAt: r.expiresAt.Time,
	}

	if r.reason != nil {
		quote.Reason = *r.reason
	}

	if r.cost != nil {
		quote.Cost = money.FromKopecks(*r.cost)
		quote.MinDays = *r.minDays
		quote.MaxDays = *r.maxDays
	}

	return &quote
}
