package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Чтение пачки приёма: одно обращение на список ключей вместо запроса на товар.
const (
	selectProductBatch = `SELECT ` + productColumns + `
		FROM unnest($1::text[], $2::text[]) AS k(tenant_id, product_id)
		JOIN catalog_products p ON p.tenant_id = k.tenant_id AND p.product_id = k.product_id`
	selectVerdictBatch = `SELECT ` + verdictColumns + `
		FROM moderation_verdicts
		WHERE (tenant_id, product_id) IN (SELECT * FROM unnest($1::text[], $2::text[]))`
	selectOpenCaseBatch = `SELECT ` + caseColumns + `
		FROM moderation_cases
		WHERE status = 'open' AND (tenant_id, product_id) IN (SELECT * FROM unnest($1::text[], $2::text[]))`

	// saveProductBatch заводит новые товары пачки и обновляет прежние условно по исходной версии.
	// Число записанных строк меньше пачки — конфликт, транзакция откатывается целиком.
	saveProductBatch = `
		WITH rows AS (
			SELECT * FROM jsonb_to_recordset($1::jsonb) AS r(
				tenant_id text, product_id text, revision bigint, deleted boolean, card jsonb, price bigint,
				base_price bigint, available integer, projected_at timestamptz, version bigint,
				updated_at timestamptz, sales_count bigint, average_rating numeric, ratings_count bigint,
				origin bigint)
		), inserted AS (
			INSERT INTO catalog_products (
				tenant_id,
				product_id,
				revision,
				deleted,
				card,
				price,
				base_price,
				available,
				projected_at,
				version,
				updated_at,
				sales_count,
				average_rating,
				ratings_count
			)
			SELECT tenant_id, product_id, revision, deleted, card, price, base_price, available, projected_at,
				version, updated_at, sales_count, average_rating, ratings_count
			FROM rows
			WHERE origin = 0
			ON CONFLICT DO NOTHING
			RETURNING 1
		), updated AS (
			UPDATE catalog_products p
			SET revision = r.revision,
				deleted = r.deleted,
				card = r.card,
				price = r.price,
				base_price = r.base_price,
				available = r.available,
				projected_at = r.projected_at,
				version = r.version,
				updated_at = r.updated_at,
				sales_count = r.sales_count,
				average_rating = r.average_rating,
				ratings_count = r.ratings_count
			FROM rows r
			WHERE r.origin > 0 AND p.tenant_id = r.tenant_id AND p.product_id = r.product_id AND p.version = r.origin
			RETURNING 1
		)
		SELECT (SELECT count(*) FROM inserted) + (SELECT count(*) FROM updated)`

	// saveVerdictBatch — то же для вердиктов: обновление условно по исходным версии и статусу.
	// decision приходит текстом JSON, как в одиночном сохранении: пустое решение — jsonb null.
	saveVerdictBatch = `
		WITH rows AS (
			SELECT * FROM jsonb_to_recordset($1::jsonb) AS r(
				tenant_id text, product_id text, revision bigint, rules_revision bigint, taxonomy_version bigint,
				status text, reasons jsonb, decided_by text, manual_reject boolean, version bigint,
				updated_at timestamptz, operator_id text, operator_name text, decision text, origin bigint,
				origin_status text)
		), inserted AS (
			INSERT INTO moderation_verdicts (
				tenant_id,
				product_id,
				revision,
				rules_revision,
				taxonomy_version,
				status,
				reasons,
				decided_by,
				manual_reject,
				version,
				updated_at,
				operator_id,
				operator_name,
				decision
			)
			SELECT tenant_id, product_id, revision, rules_revision, taxonomy_version, status, reasons, decided_by,
				manual_reject, version, updated_at, operator_id, operator_name, decision::jsonb
			FROM rows
			WHERE origin = 0
			ON CONFLICT DO NOTHING
			RETURNING 1
		), updated AS (
			UPDATE moderation_verdicts v
			SET revision = r.revision,
				rules_revision = r.rules_revision,
				taxonomy_version = r.taxonomy_version,
				status = r.status,
				reasons = r.reasons,
				decided_by = r.decided_by,
				manual_reject = r.manual_reject,
				version = r.version,
				updated_at = r.updated_at,
				operator_id = r.operator_id,
				operator_name = r.operator_name,
				decision = r.decision::jsonb
			FROM rows r
			WHERE r.origin > 0 AND v.tenant_id = r.tenant_id AND v.product_id = r.product_id
				AND v.version = r.origin AND v.status = r.origin_status
			RETURNING 1
		)
		SELECT (SELECT count(*) FROM inserted) + (SELECT count(*) FROM updated)`
)

// productWrite — строка пачки товаров; времена усечены до микросекунд, как их пишет pgx.
type productWrite struct {
	TenantID      string          `json:"tenant_id"`
	ProductID     string          `json:"product_id"`
	Revision      int64           `json:"revision"`
	Deleted       bool            `json:"deleted"`
	Card          json.RawMessage `json:"card"`
	Price         *int64          `json:"price"`
	BasePrice     *int64          `json:"base_price"`
	Available     int             `json:"available"`
	ProjectedAt   time.Time       `json:"projected_at"`
	Version       int64           `json:"version"`
	UpdatedAt     time.Time       `json:"updated_at"`
	SalesCount    int64           `json:"sales_count"`
	AverageRating float64         `json:"average_rating"`
	RatingsCount  int64           `json:"ratings_count"`
	Origin        int64           `json:"origin"`
}

type verdictWrite struct {
	TenantID        ids.TenantID      `json:"tenant_id"`
	ProductID       ids.ProductID     `json:"product_id"`
	Revision        int64             `json:"revision"`
	RulesRevision   int64             `json:"rules_revision"`
	TaxonomyVersion int64             `json:"taxonomy_version"`
	Status          moderation.Status `json:"status"`
	Reasons         json.RawMessage   `json:"reasons"`
	DecidedBy       string            `json:"decided_by"`
	ManualReject    bool              `json:"manual_reject"`
	Version         int64             `json:"version"`
	UpdatedAt       time.Time         `json:"updated_at"`
	OperatorID      string            `json:"operator_id"`
	OperatorName    string            `json:"operator_name"`
	Decision        string            `json:"decision"`
	Origin          int64             `json:"origin"`
	OriginStatus    moderation.Status `json:"origin_status"`
}

// GetMany читает товары списка одним запросом по первичному ключу.
func (r *Products) GetMany(ctx context.Context, keys []ids.OfferID) (map[ids.OfferID]*catalog.Product, error) {
	// Одиночный снимок — простой запрос по ключу: без разбора массивов и соединения.
	if len(keys) == 1 {
		product, err := r.Get(ctx, keys[0].Tenant, keys[0].Product)
		if errors.Is(err, catalog.ErrProductNotFound) {
			return map[ids.OfferID]*catalog.Product{}, nil
		}
		return map[ids.OfferID]*catalog.Product{keys[0]: product}, err
	}
	tenants, products := offerKeyColumns(keys)
	rows, err := rawSQL(ctx, r.db, selectProductBatch, tenants, products).Rows()
	if err != nil {
		return nil, fmt.Errorf("пачка товаров: %w", err)
	}
	defer func() { _ = rows.Close() }()
	found := make(map[ids.OfferID]*catalog.Product, len(keys))
	for rows.Next() {
		var row productRow
		if err := rows.Scan(row.targets()...); err != nil {
			return nil, fmt.Errorf("пачка товаров: %w", err)
		}
		product, err := row.product()
		if err != nil {
			return nil, err
		}
		state := product.State()
		found[ids.OfferID{Tenant: state.TenantID, Product: state.ProductID}] = product
	}
	return found, rows.Err()
}

// GetMany читает вердикты списка одним запросом по первичному ключу.
func (r *ModerationVerdicts) GetMany(
	ctx context.Context,
	keys []ids.OfferID,
) (map[ids.OfferID]*moderation.Verdict, error) {
	if len(keys) == 1 {
		verdict, err := r.Get(ctx, keys[0].Tenant, keys[0].Product)
		if errors.Is(err, moderation.ErrNotFound) {
			return map[ids.OfferID]*moderation.Verdict{}, nil
		}
		return map[ids.OfferID]*moderation.Verdict{keys[0]: verdict}, err
	}
	tenants, products := offerKeyColumns(keys)
	rows, err := rawSQL(ctx, r.db, selectVerdictBatch, tenants, products).Rows()
	if err != nil {
		return nil, fmt.Errorf("пачка вердиктов: %w", err)
	}
	defer func() { _ = rows.Close() }()
	found := make(map[ids.OfferID]*moderation.Verdict, len(keys))
	for rows.Next() {
		verdict, err := scanModerationVerdict(rows)
		if err != nil {
			return nil, fmt.Errorf("пачка вердиктов: %w", err)
		}
		state := verdict.State()
		found[ids.OfferID{Tenant: state.TenantID, Product: state.ProductID}] = verdict
	}
	return found, rows.Err()
}

// OpenMany читает открытые дела списка одним запросом по индексу единственного открытого дела.
func (r *ModerationCases) OpenMany(ctx context.Context, keys []ids.OfferID) (map[ids.OfferID]*moderation.Case, error) {
	if len(keys) == 1 {
		open, err := r.Open(ctx, keys[0].Tenant, keys[0].Product)
		if errors.Is(err, moderation.ErrNotFound) {
			return map[ids.OfferID]*moderation.Case{}, nil
		}
		return map[ids.OfferID]*moderation.Case{keys[0]: open}, err
	}
	tenants, products := offerKeyColumns(keys)
	rows, err := rawSQL(ctx, r.db, selectOpenCaseBatch, tenants, products).Rows()
	if err != nil {
		return nil, fmt.Errorf("пачка дел: %w", err)
	}
	defer func() { _ = rows.Close() }()
	found := make(map[ids.OfferID]*moderation.Case, len(keys))
	for rows.Next() {
		open, err := scanModerationCase(rows)
		if err != nil {
			return nil, fmt.Errorf("пачка дел: %w", err)
		}
		state := open.State()
		found[ids.OfferID{Tenant: state.TenantID, Product: state.ProductID}] = open
	}
	return found, rows.Err()
}

// SaveMany пишет товары пачки одним оператором; конфликт любой строки — ErrConflict.
func (r *Products) SaveMany(ctx context.Context, products []*catalog.Product) error {
	switch len(products) {
	case 0:
		return nil
	case 1:
		return r.Save(ctx, products[0])
	}
	rows := make([]productWrite, 0, len(products))
	for _, product := range products {
		row, err := productWriteOf(product)
		if err != nil {
			return err
		}
		rows = append(rows, row)
	}
	encoded, err := json.Marshal(rows)
	if err != nil {
		return fmt.Errorf("пачка товаров: %w", err)
	}
	var written int64
	if err := rawSQL(ctx, r.db, saveProductBatch, string(encoded)).Row().Scan(&written); err != nil {
		return fmt.Errorf("пачка товаров: %w", err)
	}
	if written != int64(len(products)) {
		return catalog.ErrConflict
	}
	for _, product := range products {
		product.MarkSaved()
	}
	return nil
}

// SaveMany пишет вердикты пачки одним оператором; конфликт любой строки — ErrConflict.
func (r *ModerationVerdicts) SaveMany(ctx context.Context, verdicts []*moderation.Verdict) error {
	switch len(verdicts) {
	case 0:
		return nil
	case 1:
		return r.Save(ctx, verdicts[0])
	}
	rows := make([]verdictWrite, 0, len(verdicts))
	for _, verdict := range verdicts {
		row, err := verdictWriteOf(verdict)
		if err != nil {
			return err
		}
		rows = append(rows, row)
	}
	encoded, err := json.Marshal(rows)
	if err != nil {
		return fmt.Errorf("пачка вердиктов: %w", err)
	}
	var written int64
	if err := rawSQL(ctx, r.db, saveVerdictBatch, string(encoded)).Row().Scan(&written); err != nil {
		return fmt.Errorf("пачка вердиктов: %w", err)
	}
	if written != int64(len(verdicts)) {
		return moderation.ErrConflict
	}
	for _, verdict := range verdicts {
		verdict.MarkSaved()
	}
	return nil
}

func productWriteOf(product *catalog.Product) (productWrite, error) {
	s := product.State()
	row := productWrite{
		TenantID:      string(s.TenantID),
		ProductID:     string(s.ProductID),
		Revision:      s.Revision,
		Deleted:       s.Deleted,
		Card:          json.RawMessage("null"),
		Available:     s.Available,
		ProjectedAt:   s.ProjectedAt.Truncate(time.Microsecond),
		Version:       s.Version,
		UpdatedAt:     s.UpdatedAt.Truncate(time.Microsecond),
		SalesCount:    s.SalesCount,
		AverageRating: s.AverageRating,
		RatingsCount:  s.RatingsCount,
		Origin:        product.OriginVersion(),
	}
	if s.Deleted {
		return row, nil
	}
	card, err := encodeCard(s.Card)
	if err != nil {
		return productWrite{}, fmt.Errorf("карточка %s/%s: %w", s.TenantID, s.ProductID, err)
	}
	row.Card = card
	if s.HasPrice {
		price, base := s.Price.Kopecks(), s.BasePrice.Kopecks()
		row.Price, row.BasePrice = &price, &base
	}
	return row, nil
}

func verdictWriteOf(verdict *moderation.Verdict) (verdictWrite, error) {
	s := verdict.State()
	reasons, err := json.Marshal(s.Reasons)
	if err != nil {
		return verdictWrite{}, err
	}
	decision, err := json.Marshal(s.Decision)
	if err != nil {
		return verdictWrite{}, err
	}
	return verdictWrite{
		TenantID:        s.TenantID,
		ProductID:       s.ProductID,
		Revision:        s.Revision,
		RulesRevision:   s.RulesRevision,
		TaxonomyVersion: s.TaxonomyVersion,
		Status:          s.Status,
		Reasons:         reasons,
		DecidedBy:       s.DecidedBy,
		ManualReject:    s.ManualReject,
		Version:         s.Version,
		UpdatedAt:       s.UpdatedAt.Truncate(time.Microsecond),
		OperatorID:      s.Operator.ID,
		OperatorName:    s.Operator.Name,
		Decision:        string(decision),
		Origin:          verdict.Origin().Version,
		OriginStatus:    verdict.Origin().Status,
	}, nil
}

func offerKeyColumns(keys []ids.OfferID) ([]string, []string) {
	tenants := make([]string, 0, len(keys))
	products := make([]string, 0, len(keys))
	for _, key := range keys {
		tenants = append(tenants, string(key.Tenant))
		products = append(products, string(key.Product))
	}
	return tenants, products
}
