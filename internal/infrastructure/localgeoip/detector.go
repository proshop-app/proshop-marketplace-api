// Package localgeoip определяет город по опубликованным данным, не сохраняя адреса посетителей.
package localgeoip

import (
	"context"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"sync"
	"time"

	"github.com/oschwald/maxminddb-golang"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/geocity"
	"github.com/proshop-app/proshop-marketplace/pkg/ipnet"
)

// ReloadInterval ограничивает stat одним обращением за пять минут, а не на каждого посетителя.
const ReloadInterval = 5 * time.Minute

// Detector держит один mmap; замена файла не прерывает текущие чтения.
type Detector struct {
	reloadMu       sync.Mutex
	mu             sync.RWMutex
	reader         *maxminddb.Reader
	info           os.FileInfo
	path           string
	devIP          netip.Addr
	catalog        *geocity.Catalog
	logger         *slog.Logger
	failed         bool
	build          prometheus.Gauge
	reloadFailures prometheus.Counter
}

type coordinates struct {
	Latitude  float64 `maxminddb:"latitude"`
	Longitude float64 `maxminddb:"longitude"`
}

type record struct {
	Country struct {
		Code string `maxminddb:"iso_code"`
	} `maxminddb:"country"`
	Location *coordinates `maxminddb:"location"`
}

// New допускает отсутствие базы: ручной выбор города продолжает работать.
func New(path string, devIP netip.Addr, logger *slog.Logger, registry prometheus.Registerer) (*Detector, error) {
	catalog, err := geocity.New()
	if err != nil {
		return nil, err
	}
	detector := &Detector{
		path:    path,
		devIP:   devIP,
		catalog: catalog,
		logger:  logger,
		build: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "marketplace_geoip_database_build_timestamp_seconds",
			Help: "Дата сборки действующей DB-IP (0 — файл ещё не загружен). Возраст: time() - значение.",
		}),
		reloadFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "marketplace_geoip_reload_failures_total",
			Help: "Неудачные открытия обновлённой базы; прежний mmap остаётся рабочим.",
		}),
	}
	registry.MustRegister(detector.build, detector.reloadFailures)
	detector.Reload()
	return detector, nil
}

// DetectCity не делает сетевых запросов, не записывает IP и не обращается к файловой системе.
func (d *Detector) DetectCity(ctx context.Context, input string) (ports.DetectedCity, error) {
	if err := ctx.Err(); err != nil {
		return ports.DetectedCity{}, err
	}
	addr, err := netip.ParseAddr(input)
	if err != nil {
		return ports.DetectedCity{}, nil
	}
	addr = addr.Unmap()
	if !ipnet.Public(addr) {
		if !ipnet.Private(addr) || !d.devIP.IsValid() {
			return ports.DetectedCity{}, nil
		}
		addr = d.devIP
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.reader == nil {
		return ports.DetectedCity{}, ports.ErrCityUnavailable
	}
	var value record
	// AsSlice ссылается на локальное значение netip; адрес не покидает этот вызов.
	if err := d.reader.Lookup(net.IP(addr.AsSlice()), &value); err != nil {
		return ports.DetectedCity{}, ports.ErrCityUnavailable
	}
	if value.Country.Code != "RU" {
		return ports.DetectedCity{Country: value.Country.Code}, nil
	}
	if value.Location == nil {
		return ports.DetectedCity{}, nil
	}
	return d.catalog.Nearest(value.Location.Latitude, value.Location.Longitude), nil
}

// Reload открывает новый inode до замены старого. Неполный/битый файл не отключает рабочую базу.
func (d *Detector) Reload() {
	d.reloadMu.Lock()
	defer d.reloadMu.Unlock()
	info, err := os.Stat(d.path)
	if err != nil {
		d.failure(err)
		return
	}
	d.mu.RLock()
	unchanged := d.info != nil && os.SameFile(d.info, info) && d.info.ModTime().Equal(info.ModTime())
	d.mu.RUnlock()
	if unchanged {
		d.failed = false
		return
	}
	reader, err := maxminddb.Open(d.path)
	if err == nil {
		err = validateMetadata(reader)
	}
	if err != nil {
		if reader != nil {
			_ = reader.Close()
		}
		d.failure(err)
		return
	}
	d.mu.Lock()
	old := d.reader
	d.reader, d.info = reader, info
	d.build.Set(float64(reader.Metadata.BuildEpoch))
	d.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	d.failed = false
	d.logger.Info("локальная база городов загружена", slog.Uint64("buildEpoch", uint64(reader.Metadata.BuildEpoch)))
}

// Run проверяет обновление независимо от запросов покупателей.
func (d *Detector) Run(ctx context.Context) error {
	timer := time.NewTicker(ReloadInterval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			d.Reload()
		}
	}
}

// Close вызывается после завершения HTTP и фонового обновления.
func (d *Detector) Close() error {
	d.reloadMu.Lock()
	defer d.reloadMu.Unlock()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.reader == nil {
		return nil
	}
	err := d.reader.Close()
	d.reader = nil
	return err
}

func (d *Detector) failure(err error) {
	d.reloadFailures.Inc()
	if !d.failed {
		d.logger.Warn(
			"локальная база городов недоступна; сохранена прежняя, доступен ручной выбор",
			slog.Any("error", err),
		)
	}
	d.failed = true
}
