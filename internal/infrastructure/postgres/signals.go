package postgres

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

const (
	signalsColumns = `
		o.tenant_id, o.product_id, o.available, o.stock_at, o.price, o.price_at, o.version, o.updated_at`

	selectSignals = `
		SELECT ` + signalsColumns + `
		FROM offers o
		WHERE o.tenant_id = $1 AND o.product_id = $2`

	insertSignals = `
		INSERT INTO offers (tenant_id, product_id, available, stock_at, price, price_at, version, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`

	updateSignals = `
		UPDATE offers
		SET available = $3,
			stock_at = $4,
			price = $5,
			price_at = $6,
			version = $7,
			updated_at = $8
		WHERE tenant_id = $1 AND product_id = $2 AND version = $9`
)

// Signals — проекция остатков и цен в PostgreSQL.
type Signals struct {
	db *gorm.DB
}

// signalsRow — строка сигналов; в модели чтения предложений её может не быть вовсе.
type signalsRow struct {
	tenantID, productID *string
	available           *int
	stockAt             *time.Time
	price               *int64
	priceAt             *time.Time
	version             *int64
	updatedAt           *time.Time
}

// Get — сигналы товара.
func (r *Signals) Get(ctx context.Context, tenantID ids.TenantID, productID ids.ProductID) (*catalog.Signals, error) {
	var row signalsRow

	err := rawSQL(ctx, r.db, selectSignals, string(tenantID), string(productID)).Row().Scan(row.targets()...)
	if notFound(err) {
		return nil, catalog.ErrSignalsNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("сигналы %s/%s: %w", tenantID, productID, err)
	}

	return row.signals(), nil
}

// Save заводит сигналы или обновляет их условно по версии.
func (r *Signals) Save(ctx context.Context, signals *catalog.Signals) error {
	s := signals.State()

	var available, stockAt, price, priceAt any

	if s.HasStock {
		available = s.Available
		stockAt = nullTime(s.StockAt)
	}

	if s.HasPrice {
		price = s.Price.Kopecks()
		priceAt = nullTime(s.PriceAt)
	}

	args := []any{
		string(s.TenantID),
		string(s.ProductID),
		available,
		stockAt,
		price,
		priceAt,
		s.Version,
		s.UpdatedAt,
	}

	if signals.OriginVersion() == 0 {
		_, err := execSQL(ctx, r.db, insertSignals, args...)
		conflict := isUnique(err, "offers_pkey")

		return saved(err, conflict, signals.MarkSaved, "сигналы", s.TenantID, s.ProductID)
	}

	affected, err := execSQL(ctx, r.db, updateSignals, append(args, signals.OriginVersion())...)
	conflict := err == nil && affected == 0

	return saved(err, conflict, signals.MarkSaved, "сигналы", s.TenantID, s.ProductID)
}

func (r *signalsRow) targets() []any {
	return []any{
		&r.tenantID,
		&r.productID,
		&r.available,
		&r.stockAt,
		&r.price,
		&r.priceAt,
		&r.version,
		&r.updatedAt,
	}
}

func (r *signalsRow) signals() *catalog.Signals {
	if r.tenantID == nil {
		return nil
	}

	state := catalog.SignalsState{
		TenantID:  ids.TenantID(*r.tenantID),
		ProductID: ids.ProductID(*r.productID),
		Version:   *r.version,
		UpdatedAt: *r.updatedAt,
	}

	if r.available != nil {
		state.HasStock = true
		state.Available = *r.available
		state.StockAt = *r.stockAt
	}

	if r.price != nil {
		state.HasPrice = true
		state.Price = money.FromKopecks(*r.price)
		state.PriceAt = *r.priceAt
	}

	return catalog.RestoreSignals(state)
}
