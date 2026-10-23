package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
)

// PickupPoints — справочник пунктов выдачи в PostgreSQL.
type PickupPoints struct {
	q querier
}

// Upsert записывает пачку одним запросом.
func (r *PickupPoints) Upsert(ctx context.Context, points []delivery.PickupPoint) error {
	if len(points) == 0 {
		return nil
	}

	n := len(points)
	carriers, codes, names, addresses, cities := make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n)
	lats, lons, schedules, payloads := make([]float64, n), make([]float64, n), make([]string, n), make([]*string, n)
	active, refreshed := make([]bool, n), make([]time.Time, n)

	for i, p := range points {
		carriers[i], codes[i], names[i], addresses[i], cities[i] = string(p.Carrier), p.Code, p.Name, p.Address, p.City
		lats[i], lons[i], schedules[i], active[i], refreshed[i] = p.Location.Lat, p.Location.Lon, p.Schedule, p.Active, p.RefreshedAt

		if len(p.Payload) > 0 {
			payload := string(p.Payload)
			payloads[i] = &payload
		}
	}

	_, err := r.q.Exec(ctx, `INSERT INTO pickup_points
		(carrier, code, name, address, city, lat, lon, schedule, payload, active, refreshed_at)
		SELECT * FROM unnest($1::text[], $2::text[], $3::text[], $4::text[], $5::text[], $6::float8[], $7::float8[], $8::text[],
			$9::jsonb[], $10::bool[], $11::timestamptz[])
		ON CONFLICT (carrier, code) DO UPDATE SET name = EXCLUDED.name, address = EXCLUDED.address, city = EXCLUDED.city,
			lat = EXCLUDED.lat, lon = EXCLUDED.lon, schedule = EXCLUDED.schedule, payload = EXCLUDED.payload,
			active = EXCLUDED.active, refreshed_at = EXCLUDED.refreshed_at`,
		carriers, codes, names, addresses, cities, lats, lons, schedules, payloads, active, refreshed)
	if err != nil {
		return fmt.Errorf("пункты выдачи: %w", err)
	}

	return nil
}

// DeactivateStale закрывает пункты, не пришедшие в обновлении, начатом в since.
func (r *PickupPoints) DeactivateStale(ctx context.Context, carrier delivery.Carrier, since time.Time) (int64, error) {
	tag, err := r.q.Exec(ctx, `UPDATE pickup_points SET active = false WHERE carrier = $1 AND active AND refreshed_at < $2`, string(carrier), since)
	if err != nil {
		return 0, fmt.Errorf("закрытие пунктов %s: %w", carrier, err)
	}

	return tag.RowsAffected(), nil
}

const pointColumns = `carrier, code, name, address, city, lat, lon, schedule, payload, active, refreshed_at`

func scanPoint(row rowScanner) (delivery.PickupPoint, error) {
	var (
		p       delivery.PickupPoint
		carrier string
		payload []byte
	)

	if err := row.Scan(&carrier, &p.Code, &p.Name, &p.Address, &p.City, &p.Location.Lat, &p.Location.Lon, &p.Schedule,
		&payload, &p.Active, &p.RefreshedAt); err != nil {
		return delivery.PickupPoint{}, err
	}

	p.Carrier, p.Payload = delivery.Carrier(carrier), payload

	return p, nil
}

// Get — пункт.
func (r *PickupPoints) Get(ctx context.Context, carrier delivery.Carrier, code string) (delivery.PickupPoint, error) {
	p, err := scanPoint(r.q.QueryRow(ctx, `SELECT `+pointColumns+` FROM pickup_points WHERE carrier = $1 AND code = $2`, string(carrier), code))
	if notFound(err) {
		return delivery.PickupPoint{}, delivery.ErrPickupPointNotFound
	}
	if err != nil {
		return delivery.PickupPoint{}, fmt.Errorf("пункт выдачи %s/%s: %w", carrier, code, err)
	}

	return p, nil
}

// Page — пункты перевозчика после кода after.
func (r *PickupPoints) Page(ctx context.Context, carrier delivery.Carrier, after string, limit int) ([]delivery.PickupPoint, error) {
	rows, err := r.q.Query(ctx, `SELECT `+pointColumns+` FROM pickup_points WHERE carrier = $1 AND code > $2 ORDER BY code LIMIT $3`,
		string(carrier), after, limit)
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

// Leases — аренда фоновых задач по часам базы.
type Leases struct {
	pool *pgxpool.Pool
}

// NewLeases — аренда поверх пула.
func NewLeases(pool *pgxpool.Pool) *Leases {
	return &Leases{pool: pool}
}

// Acquire берёт свободную, истёкшую или свою аренду.
func (l *Leases) Acquire(ctx context.Context, name, holder string, ttl time.Duration) (bool, error) {
	tag, err := l.pool.Exec(ctx, `INSERT INTO job_leases (name, holder, until) VALUES ($1, $2, now() + make_interval(secs => $3))
		ON CONFLICT (name) DO UPDATE SET holder = EXCLUDED.holder, until = EXCLUDED.until
		WHERE job_leases.until < now() OR job_leases.holder = EXCLUDED.holder`, name, holder, ttl.Seconds())
	if err != nil {
		return false, fmt.Errorf("аренда %s: %w", name, err)
	}

	return tag.RowsAffected() == 1, nil
}

// Release отпускает свою аренду.
func (l *Leases) Release(ctx context.Context, name, holder string) error {
	if _, err := l.pool.Exec(ctx, `DELETE FROM job_leases WHERE name = $1 AND holder = $2`, name, holder); err != nil {
		return fmt.Errorf("аренда %s: %w", name, err)
	}

	return nil
}
