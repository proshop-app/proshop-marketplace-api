// Package geocity сопоставляет координаты с зафиксированным справочником городов.
package geocity

import (
	_ "embed"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/regions"
)

const (
	earthRadiusKM = 6371.0088
	// На 135 публичных домашних probes RIPE Atlas p95 расстояния DB-IP → город = 45 км.
	// 50 км оставляет эту область, но отсекает далёкие координаты; это не гарантия точности GeoIP.
	maxDistanceKM = 50
)

//go:embed data/city.csv
var cityCSV string

// Catalog — неизменяемое дерево единичных векторов (расстояние на сфере).
type Catalog struct {
	root *node
}

type entry struct {
	city  ports.DetectedCity
	point [3]float64
}

// New проверяет поставку и один раз строит пространственный индекс.
func New() (*Catalog, error) {
	r := csv.NewReader(strings.NewReader(cityCSV))
	if _, err := r.Read(); err != nil {
		return nil, err
	}
	var entries []entry
	for {
		row, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		item, ok, err := parse(row)
		if err != nil {
			return nil, err
		}
		if ok {
			entries = append(entries, item)
		}
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("пустой справочник городов")
	}
	return &Catalog{root: build(entries, 0)}, nil
}

func parse(row []string) (entry, bool, error) {
	if len(row) != 24 || len(row[12]) < 2 {
		return entry{}, false, fmt.Errorf("неверная строка справочника городов")
	}
	code, ok := regions.FromKladr(row[12][:2])
	if !ok {
		return entry{}, false, nil
	}
	lat, latErr := strconv.ParseFloat(row[20], 64)
	lon, lonErr := strconv.ParseFloat(row[21], 64)
	if latErr != nil || lonErr != nil || !validCoordinates(lat, lon) {
		return entry{}, false, fmt.Errorf("неверные координаты города в поставке")
	}
	name := row[9]
	if name == "" && row[4] == "г" {
		name = row[5]
	}
	if name == "" && row[6] == "г" {
		name = row[7]
	}
	if name == "" {
		name = row[11]
	}
	if name == "" || row[13] == "" {
		return entry{}, false, fmt.Errorf("нет имени или ФИАС города")
	}
	region, _ := regions.Name(code)
	return entry{
		city: ports.DetectedCity{
			City:       name,
			Country:    "RU",
			Label:      row[0],
			Region:     region,
			RegionCode: code,
			FiasID:     row[13],
			KladrID:    row[12],
		},
		point: vector(lat, lon),
	}, true, nil
}

// Nearest возвращает только город в пределах проверенного радиуса.
func (c *Catalog) Nearest(lat, lon float64) ports.DetectedCity {
	if !validCoordinates(lat, lon) {
		return ports.DetectedCity{}
	}
	point := vector(lat, lon)
	best, squared := c.root.nearest(point, c.root.item, math.Inf(1))
	limit := 2 * math.Sin(maxDistanceKM/(2*earthRadiusKM))
	if squared > limit*limit {
		return ports.DetectedCity{}
	}
	return best.city
}

func validCoordinates(lat, lon float64) bool {
	return !math.IsNaN(lat) && !math.IsNaN(lon) && lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180
}

func vector(lat, lon float64) [3]float64 {
	lat *= math.Pi / 180
	lon *= math.Pi / 180
	return [3]float64{math.Cos(lat) * math.Cos(lon), math.Cos(lat) * math.Sin(lon), math.Sin(lat)}
}
