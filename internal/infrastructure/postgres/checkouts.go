package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

const (
	checkoutColumns = `
		id, buyer_id, idempotency_key, status, carrier, pickup_point_id, delivery, goods_total, delivery_total,
		total, version, created_at, updated_at, confirmed_at, order_context`

	// selectCheckout дополняется условием отбора.
	selectCheckout = `
		SELECT ` + checkoutColumns + `
		FROM checkouts
		WHERE `

	insertCheckout = `
		INSERT INTO checkouts (` + checkoutColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`

	updateCheckout = `
		UPDATE checkouts
		SET status = $4,
			carrier = $5,
			pickup_point_id = $6,
			delivery = $7,
			goods_total = $8,
			delivery_total = $9,
			total = $10,
			version = $11,
			updated_at = $12,
			confirmed_at = $13,
			order_context = $14
		WHERE id = $1 AND status = $2 AND version = $3`

	groupColumns = `
		tenant_id, external_checkout_id, lines, goods, quote_status, quote_reason, delivery_cost,
		subsidy, buyer_delivery, min_days, max_days, quote_expires_at, order_progress`

	selectCheckoutGroups = `
		SELECT ` + groupColumns + `
		FROM checkout_groups
		WHERE checkout_id = $1
   AND EXISTS (SELECT 1 FROM checkouts WHERE id = $1 AND version = $2)
  ORDER BY tenant_id`

	insertCheckoutGroup = `
		INSERT INTO checkout_groups (checkout_id, ` + groupColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`

	updateCheckoutGroup = `
		UPDATE checkout_groups
		SET external_checkout_id = $3,
			lines = $4,
			goods = $5,
			quote_status = $6,
			quote_reason = $7,
			delivery_cost = $8,
			subsidy = $9,
			buyer_delivery = $10,
			min_days = $11,
			max_days = $12,
			quote_expires_at = $13,
			order_progress = $14
		WHERE checkout_id = $1 AND tenant_id = $2`
)

// Checkouts — покупки в PostgreSQL.
type Checkouts struct {
	q querier
}

// lineJSON — позиция снимка в колонке jsonb; цена — копейки.
type lineJSON struct {
	ProductID string `json:"productId"`
	Name      string `json:"name"`
	Quantity  int    `json:"quantity"`
	Price     int64  `json:"price"`
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
	confirmedAt         *time.Time
	orderContext        []byte
}

// groupRow — строка группы; поля расчёта пусты, пока доставку не считали.
type groupRow struct {
	group                        checkout.Group
	tenantID                     string
	lines                        []byte
	goods                        int64
	quoteStatus, reason          *string
	cost, subsidy, buyerDelivery *int64
	minDays, maxDays             *int
	expiresAt                    *time.Time
	orderProgress                []byte
}

// Get — покупка.
func (r *Checkouts) Get(ctx context.Context, id ids.UUID) (*checkout.Checkout, error) {
	return r.one(ctx, `id = $1`, string(id))
}

// ByKey — покупка покупателя по ключу идемпотентности.
func (r *Checkouts) ByKey(ctx context.Context, buyerID ids.UUID, key string) (*checkout.Checkout, error) {
	return r.one(ctx, `buyer_id = $1 AND idempotency_key = $2`, string(buyerID), key)
}

// Add сохраняет новую покупку с группами.
func (r *Checkouts) Add(ctx context.Context, c *checkout.Checkout) error {
	s := c.State()

	carrier, point, selection, err := selectionArgs(s.Delivery)
	if err != nil {
		return err
	}

	_, err = r.q.Exec(
		ctx,
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
	)
	if isUnique(err, "checkouts_buyer_key_key") {
		return checkout.ErrDuplicateKey
	}
	if err != nil {
		return fmt.Errorf("новая покупка: %w", err)
	}

	if err := r.writeGroups(ctx, insertCheckoutGroup, s); err != nil {
		return err
	}

	c.MarkSaved()

	return nil
}

// Update сохраняет условно по исходным статусу и версии; группы переписываются.
func (r *Checkouts) Update(ctx context.Context, c *checkout.Checkout) error {
	s := c.State()
	status, version := c.Origin()

	carrier, point, selection, err := selectionArgs(s.Delivery)
	if err != nil {
		return err
	}

	tag, err := r.q.Exec(
		ctx,
		updateCheckout,
		string(s.ID),
		string(status),
		version,
		string(s.Status),
		carrier,
		point,
		selection,
		s.GoodsTotal.Kopecks(),
		s.DeliveryTotal.Kopecks(),
		s.Total.Kopecks(),
		s.Version,
		s.UpdatedAt,
		nullTime(s.ConfirmedAt),
		encodeOrderContext(s),
	)
	if err != nil {
		return fmt.Errorf("покупка %s: %w", s.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return checkout.ErrConflict
	}

	if err := r.writeGroups(ctx, updateCheckoutGroup, s); err != nil {
		return err
	}

	c.MarkSaved()

	return nil
}

func (r *Checkouts) one(ctx context.Context, where string, args ...any) (*checkout.Checkout, error) {
	var row checkoutRow

	err := r.q.QueryRow(ctx, selectCheckout+where, args...).Scan(row.targets()...)
	if notFound(err) {
		return nil, checkout.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("покупка: %w", err)
	}

	state, err := row.checkout()
	if err != nil {
		return nil, err
	}

	if state.Groups, err = r.groups(ctx, row.id, state.Version); err != nil {
		return nil, err
	}

	return checkout.Restore(state), nil
}

func (r *Checkouts) groups(ctx context.Context, checkoutID string, version int64) ([]checkout.Group, error) {
	rows, err := r.q.Query(ctx, selectCheckoutGroups, checkoutID, version)
	if err != nil {
		return nil, fmt.Errorf("группы покупки: %w", err)
	}
	defer rows.Close()

	var groups []checkout.Group

	for rows.Next() {
		var row groupRow
		if err := rows.Scan(row.targets()...); err != nil {
			return nil, fmt.Errorf("группы покупки: %w", err)
		}

		group, err := row.checkoutGroup()
		if err != nil {
			return nil, fmt.Errorf("позиции покупки %s: %w", checkoutID, err)
		}

		groups = append(groups, group)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(groups) == 0 {
		return nil, checkout.ErrConflict
	}
	return groups, nil
}

// writeGroups пишет группы покупки запросом вставки или обновления: у обоих
// первые два параметра — покупка и магазин, дальше поля группы.
func (r *Checkouts) writeGroups(ctx context.Context, sql string, s checkout.State) error {
	for _, group := range s.Groups {
		args, err := groupArgs(group)
		if err != nil {
			return err
		}

		if _, err := r.q.Exec(ctx, sql, append([]any{string(s.ID)}, args...)...); err != nil {
			return fmt.Errorf("группа покупки %s: %w", s.ID, err)
		}
	}

	return nil
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
		&r.state.CreatedAt,
		&r.state.UpdatedAt,
		&r.confirmedAt,
		&r.orderContext,
	}
}

func (r *checkoutRow) checkout() (checkout.State, error) {
	state := r.state
	var meta checkoutOrderContext
	if err := json.Unmarshal(r.orderContext, &meta); err != nil {
		return checkout.State{}, err
	}
	state.Contact, state.ReturnURL, state.CartConsumed = meta.Contact, meta.ReturnURL, meta.CartConsumed
	state.ID = ids.UUID(r.id)
	state.BuyerID = ids.UUID(r.buyerID)
	state.Status = checkout.Status(r.status)
	state.GoodsTotal = money.FromKopecks(r.goods)
	state.DeliveryTotal = money.FromKopecks(r.deliv)
	state.Total = money.FromKopecks(r.total)

	if r.confirmedAt != nil {
		state.ConfirmedAt = *r.confirmedAt
	}

	if r.carrier == nil {
		return state, nil
	}

	var doc selectionJSON
	if len(r.selection) > 0 {
		if err := json.Unmarshal(r.selection, &doc); err != nil {
			return checkout.State{}, fmt.Errorf("доставка покупки %s: %w", r.id, err)
		}
	}

	state.Delivery = &checkout.Selection{
		Carrier:         delivery.Carrier(*r.carrier),
		PickupPointCode: *r.pointID,
		City:            doc.City,
		Address:         doc.Address,
		RegionCode:      doc.RegionCode,
	}

	return state, nil
}

func (r *groupRow) targets() []any {
	return []any{
		&r.tenantID,
		&r.group.ExternalCheckoutID,
		&r.lines,
		&r.goods,
		&r.quoteStatus,
		&r.reason,
		&r.cost,
		&r.subsidy,
		&r.buyerDelivery,
		&r.minDays,
		&r.maxDays,
		&r.expiresAt,
		&r.orderProgress,
	}
}

func (r *groupRow) checkoutGroup() (checkout.Group, error) {
	group := r.group
	if err := json.Unmarshal(r.orderProgress, &group); err != nil {
		return checkout.Group{}, err
	}
	group.TenantID = ids.TenantID(r.tenantID)
	group.Goods = money.FromKopecks(r.goods)

	var docs []lineJSON
	if err := json.Unmarshal(r.lines, &docs); err != nil {
		return checkout.Group{}, err
	}

	for _, doc := range docs {
		line := checkout.Line{
			Offer:    ids.OfferID{Tenant: group.TenantID, Product: ids.ProductID(doc.ProductID)},
			Name:     doc.Name,
			Quantity: doc.Quantity,
			Price:    money.FromKopecks(doc.Price),
		}

		group.Lines = append(group.Lines, line)
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
		ExpiresAt: *r.expiresAt,
	}

	if r.reason != nil {
		quote.Reason = *r.reason
	}

	if r.cost != nil {
		quote.Cost = money.FromKopecks(*r.cost)
		quote.Subsidy = money.FromKopecks(*r.subsidy)
		quote.BuyerCost = money.FromKopecks(*r.buyerDelivery)
		quote.MinDays = *r.minDays
		quote.MaxDays = *r.maxDays
	}

	return &quote
}

func selectionArgs(selection *checkout.Selection) (carrier, point, encoded any, err error) {
	if selection == nil {
		return nil, nil, nil, nil
	}

	doc := selectionJSON{
		City:       selection.City,
		Address:    selection.Address,
		RegionCode: selection.RegionCode,
	}

	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("доставка покупки: %w", err)
	}

	return string(selection.Carrier), selection.PickupPointCode, string(raw), nil
}

// groupArgs — параметры группы после идентификатора покупки, в порядке groupColumns.
func groupArgs(group checkout.Group) ([]any, error) {
	docs := make([]lineJSON, 0, len(group.Lines))

	for _, line := range group.Lines {
		doc := lineJSON{
			ProductID: string(line.Offer.Product),
			Name:      line.Name,
			Quantity:  line.Quantity,
			Price:     line.Price.Kopecks(),
		}

		docs = append(docs, doc)
	}

	lines, err := json.Marshal(docs)
	if err != nil {
		return nil, fmt.Errorf("позиции покупки: %w", err)
	}

	args := []any{
		string(group.TenantID),
		group.ExternalCheckoutID,
		string(lines),
		group.Goods.Kopecks(),
	}

	args = append(args, quoteArgs(group.Quote)...)
	return append(args, encodeOrderProgress(group)), nil
}

// quoteArgs — поля расчёта доставки; суммы и сроки есть только у доступного расчёта.
func quoteArgs(q *delivery.Quote) []any {
	if q == nil {
		return []any{nil, nil, nil, nil, nil, nil, nil, nil}
	}

	var reason any
	if q.Reason != "" {
		reason = q.Reason
	}

	if q.Status != delivery.QuoteAvailable {
		return []any{string(q.Status), reason, nil, nil, nil, nil, nil, q.ExpiresAt}
	}

	return []any{
		string(q.Status),
		reason,
		q.Cost.Kopecks(),
		q.Subsidy.Kopecks(),
		q.BuyerCost.Kopecks(),
		q.MinDays,
		q.MaxDays,
		q.ExpiresAt,
	}
}
