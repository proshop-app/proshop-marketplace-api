package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
)

const (
	pointColumns = `carrier, code, name, address, city, lat, lon, schedule, payload, active, refreshed_at`

	upsertPoints = `
		INSERT INTO pickup_points (` + pointColumns + `)
		SELECT *
		FROM unnest(
			$1::text[],
			$2::text[],
			$3::text[],
			$4::text[],
			$5::text[],
			$6::float8[],
			$7::float8[],
			$8::text[],
			$9::jsonb[],
			$10::bool[],
			$11::timestamptz[]
		)
		ON CONFLICT (carrier, code) DO UPDATE
		SET name = EXCLUDED.name,
			address = EXCLUDED.address,
			city = EXCLUDED.city,
			lat = EXCLUDED.lat,
			lon = EXCLUDED.lon,
			schedule = EXCLUDED.schedule,
			payload = EXCLUDED.payload,
			active = EXCLUDED.active,
			refreshed_at = EXCLUDED.refreshed_at`

	deactivateStalePoints = `
		UPDATE pickup_points
		SET active = false
		WHERE carrier = $1 AND active AND refreshed_at < $2`

	selectPoint = `
		SELECT ` + pointColumns + `
		FROM pickup_points
		WHERE carrier = $1 AND code = $2`

	selectPointsPage = `
		SELECT ` + pointColumns + `
		FROM pickup_points
		WHERE carrier = $1 AND code > $2
		ORDER BY code
		LIMIT $3`
)

// PickupPoints — справочник пунктов выдачи в PostgreSQL.
type PickupPoints struct {
	q querier
}

// pointColumnsBatch — пачка пунктов по колонкам для вставки через unnest.
type pointColumnsBatch struct {
	carriers  []string
	codes     []string
	names     []string
	addresses []string
	cities    []string
	lats      []float64
	lons      []float64
	schedules []string
	payloads  []*string
	active    []bool
	refreshed []time.Time
}

// Upsert записывает пачку одним запросом.
func (r *PickupPoints) Upsert(ctx context.Context, points []delivery.PickupPoint) error {
	if len(points) == 0 {
		return nil
	}

	batch := newPointColumnsBatch(points)

	_, err := r.q.Exec(
		ctx,
		upsertPoints,
		batch.carriers,
		batch.codes,
		batch.names,
		batch.addresses,
		batch.cities,
		batch.lats,
		batch.lons,
		batch.schedules,
		batch.payloads,
		batch.active,
		batch.refreshed,
	)
	if err != nil {
		return fmt.Errorf("пункты выдачи: %w", err)
	}

	return nil
}

// DeactivateStale закрывает пункты, не пришедшие в обновлении, начатом в since.
func (r *PickupPoints) DeactivateStale(ctx context.Context, carrier delivery.Carrier, since time.Time) (int64, error) {
	tag, err := r.q.Exec(ctx, deactivateStalePoints, string(carrier), since)
	if err != nil {
		return 0, fmt.Errorf("закрытие пунктов %s: %w", carrier, err)
	}

	return tag.RowsAffected(), nil
}

// Get — пункт.
func (r *PickupPoints) Get(ctx context.Context, carrier delivery.Carrier, code string) (delivery.PickupPoint, error) {
	p, err := scanPoint(r.q.QueryRow(ctx, selectPoint, string(carrier), code))
	if notFound(err) {
		return delivery.PickupPoint{}, delivery.ErrPickupPointNotFound
	}
	if err != nil {
		return delivery.PickupPoint{}, fmt.Errorf("пункт выдачи %s/%s: %w", carrier, code, err)
	}

	return p, nil
}

// Page — пункты перевозчика после кода after.
func (r *PickupPoints) Page(
	ctx context.Context,
	carrier delivery.Carrier,
	after string,
	limit int,
) ([]delivery.PickupPoint, error) {
	rows, err := r.q.Query(ctx, selectPointsPage, string(carrier), after, limit)
	if err != nil {
		return nil, fmt.Errorf("пункты выдачи %s: %w", carrier, err)
	}
	defer rows.Close()

	var page []delivery.PickupPoint

	for rows.Next() {
		p, err := scanPoint(rows)
		if err != nil {
			return nil, fmt.Errorf("пункты выдачи %s: %w", carrier, err)
		}

		page = append(page, p)
	}

	return page, rows.Err()
}

func newPointColumnsBatch(points []delivery.PickupPoint) pointColumnsBatch {
	n := len(points)
	batch := pointColumnsBatch{
		carriers:  make([]string, n),
		codes:     make([]string, n),
		names:     make([]string, n),
		addresses: make([]string, n),
		cities:    make([]string, n),
		lats:      make([]float64, n),
		lons:      make([]float64, n),
		schedules: make([]string, n),
		payloads:  make([]*string, n),
		active:    make([]bool, n),
		refreshed: make([]time.Time, n),
	}

	for i, p := range points {
		batch.carriers[i] = string(p.Carrier)
		batch.codes[i] = p.Code
		batch.names[i] = p.Name
		batch.addresses[i] = p.Address
		batch.cities[i] = p.City
		batch.lats[i] = p.Location.Lat
		batch.lons[i] = p.Location.Lon
		batch.schedules[i] = p.Schedule
		batch.active[i] = p.Active
		batch.refreshed[i] = p.RefreshedAt

		if len(p.Payload) > 0 {
			payload := string(p.Payload)
			batch.payloads[i] = &payload
		}
	}

	return batch
}

func scanPoint(row rowScanner) (delivery.PickupPoint, error) {
	var (
		p       delivery.PickupPoint
		carrier string
		payload []byte
	)

	err := row.Scan(
		&carrier,
		&p.Code,
		&p.Name,
		&p.Address,
		&p.City,
		&p.Location.Lat,
		&p.Location.Lon,
		&p.Schedule,
		&payload,
		&p.Active,
		&p.RefreshedAt,
	)
	if err != nil {
		return delivery.PickupPoint{}, err
	}

	p.Carrier = delivery.Carrier(carrier)
	p.Payload = payload

	return p, nil
}
