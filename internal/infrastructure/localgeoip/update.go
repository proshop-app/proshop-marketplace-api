package localgeoip

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/oschwald/maxminddb-golang"
)

const (
	// Ноябрь 2026: 121 МиБ. 512 МиБ дают запас роста и ограничивают распаковку.
	maxDatabaseBytes = 512 << 20
	downloadHost     = "download.db-ip.com"
)

// Updater загружает только опубликованную DB-IP, вне пути HTTP-запроса покупателя.
type Updater struct {
	client *http.Client
	source string
}

// NewUpdater запрещает перенаправления и прокси окружения: разрешён только HTTPS хост DB-IP.
func NewUpdater() *Updater {
	return &Updater{
		source: "https://" + downloadHost + "/free/dbip-city-lite-",
		client: &http.Client{
			Timeout: 10 * time.Minute,
			Transport: &http.Transport{
				DialContext:           (&net.Dialer{Timeout: 30 * time.Second}).DialContext,
				TLSHandshakeTimeout:   15 * time.Second,
				ResponseHeaderTimeout: 30 * time.Second,
			},
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return fmt.Errorf("перенаправление загрузки DB-IP запрещено")
			},
		},
	}
}

// Update проверяет MMDB и контрольные записи до атомарной замены inode в том же каталоге.
func (u *Updater) Update(ctx context.Context, path, month string) error {
	if _, err := time.Parse("2006-01", month); err != nil {
		return fmt.Errorf("месяц DB-IP: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.source+month+".mmdb.gz", nil)
	if err != nil {
		return err
	}
	response, err := u.client.Do(request)
	if err != nil {
		return fmt.Errorf("скачивание DB-IP: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("скачивание DB-IP: HTTP %d", response.StatusCode)
	}
	zipped, err := gzip.NewReader(response.Body)
	if err != nil {
		return err
	}
	defer func() { _ = zipped.Close() }()
	file, err := os.CreateTemp(filepath.Dir(path), ".dbip-*.mmdb")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	defer func() { _ = file.Close() }()
	size, err := io.Copy(file, io.LimitReader(zipped, maxDatabaseBytes+1))
	if err != nil {
		return fmt.Errorf("распаковка DB-IP: %w", err)
	}
	if size > maxDatabaseBytes {
		return fmt.Errorf("DB-IP превышает предел распаковки")
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := validateReplacement(file.Name(), path); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func validateReplacement(path, active string) error {
	reader, err := maxminddb.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()
	if err := validateMetadata(reader); err != nil {
		return err
	}
	if err := reader.Verify(); err != nil {
		return err
	}
	// Контроль наличия записей публичных DNS (IPv4 + IPv6), а не страны anycast-узла.
	// https://developers.google.com/speed/public-dns/docs/using
	// DB-IP 2026-11 относит IPv6 Google к CA: страна anycast может меняться между поставками.
	controls := []string{"77.88.8.8", "8.8.8.8", "2001:4860:4860::8888"}
	for _, ip := range controls {
		var value record
		if err := reader.Lookup(net.ParseIP(ip), &value); err != nil {
			return err
		}
		if !located(value) {
			return fmt.Errorf("DB-IP: не пройдены контрольные записи")
		}
	}
	previous, err := maxminddb.Open(active)
	if err == nil {
		defer func() { _ = previous.Close() }()
		if reader.Metadata.BuildEpoch < previous.Metadata.BuildEpoch {
			return fmt.Errorf("DB-IP: новая поставка старше действующей")
		}
	}
	return nil
}

func located(value record) bool {
	point := value.Location
	if len(value.Country.Code) != 2 || point == nil {
		return false
	}
	return !math.IsNaN(point.Latitude) && !math.IsNaN(point.Longitude) &&
		point.Latitude >= -90 && point.Latitude <= 90 && point.Longitude >= -180 && point.Longitude <= 180 &&
		(point.Latitude != 0 || point.Longitude != 0)
}
