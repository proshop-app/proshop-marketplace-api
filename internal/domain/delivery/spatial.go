package delivery

import (
	"math"
	"slices"
	"sort"
)

// PickupMapLimit ограничивает JSON и число объектов до кластеризации на мобильном клиенте.
const PickupMapLimit = 500

// Размер ячейки ~2 км по широте: узкий viewport читает соседние ячейки, а не весь город.
const pickupCellDegrees = 0.02

// Bounds — юг, запад, север, восток в WGS84; пересечение антимеридиана задаётся двумя запросами.
type Bounds [4]float64

type pickupCell struct {
	row     int
	regions map[string][]int
}

type pickupColumn struct {
	col   int
	cells []pickupCell
}

// PickupIndex неизменяем после построения. Полные ячейки считаются по длине регионального списка.
type PickupIndex struct {
	columns []pickupColumn
	bounds  map[string]Bounds
	points  []PickupPoint
	regions map[string][]int
}

// PickupArea содержит ограниченный ответ и точное число совпадений.
type PickupArea struct {
	Items  []PickupPoint
	Total  int
	Refine bool
	Bounds *Bounds
}

// Valid проверяет конечные координаты и порядок границ.
func (b Bounds) Valid() bool {
	for _, value := range b {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	return b[0] >= -90 && b[2] <= 90 && b[1] >= -180 && b[3] <= 180 && b[0] <= b[2] && b[1] <= b[3]
}

func (b Bounds) contains(p Location) bool {
	return p.Lat >= b[0] && p.Lat <= b[2] && p.Lon >= b[1] && p.Lon <= b[3]
}

// NewPickupIndex строит сетку один раз на снимок.
func NewPickupIndex(points []PickupPoint) *PickupIndex {
	index := &PickupIndex{
		bounds:  make(map[string]Bounds),
		points:  points,
		regions: make(map[string][]int),
	}
	grid := make(map[int]map[int]map[string][]int)
	for position, point := range points {
		index.regions[point.RegionCode] = append(index.regions[point.RegionCode], position)
		col, row := pickupCellAt(point.Location.Lon), pickupCellAt(point.Location.Lat)
		if grid[col] == nil {
			grid[col] = make(map[int]map[string][]int)
		}
		if grid[col][row] == nil {
			grid[col][row] = make(map[string][]int)
		}
		grid[col][row][point.RegionCode] = append(grid[col][row][point.RegionCode], position)
		b, found := index.bounds[point.RegionCode]
		if !found {
			b = Bounds{point.Location.Lat, point.Location.Lon, point.Location.Lat, point.Location.Lon}
		}
		b[0], b[1] = min(b[0], point.Location.Lat), min(b[1], point.Location.Lon)
		b[2], b[3] = max(b[2], point.Location.Lat), max(b[3], point.Location.Lon)
		index.bounds[point.RegionCode] = b
	}
	for col, rows := range grid {
		column := pickupColumn{col: col}
		for row, regions := range rows {
			column.cells = append(column.cells, pickupCell{row: row, regions: regions})
		}
		slices.SortFunc(column.cells, func(a, b pickupCell) int { return a.row - b.row })
		index.columns = append(index.columns, column)
	}
	slices.SortFunc(index.columns, func(a, b pickupColumn) int { return a.col - b.col })
	return index
}

// Area пропускает пустые столбцы двоичным поиском; точки читает только на границе области и для ответа.
func (i *PickupIndex) Area(box Bounds, regions []string) PickupArea {
	result := PickupArea{Items: []PickupPoint{}, Bounds: i.RegionBounds(regions)}
	regions = slices.Compact(slices.Sorted(slices.Values(regions)))
	west, east := pickupCellAt(box[1]), pickupCellAt(box[3])
	south, north := pickupCellAt(box[0]), pickupCellAt(box[2])
	start := sort.Search(len(i.columns), func(n int) bool { return i.columns[n].col >= west })
	for _, column := range i.columns[start:] {
		if column.col > east {
			break
		}
		first := sort.Search(len(column.cells), func(n int) bool { return column.cells[n].row >= south })
		for _, cell := range column.cells[first:] {
			if cell.row > north {
				break
			}
			full := box[1] <= float64(column.col)*pickupCellDegrees &&
				box[3] >= float64(column.col+1)*pickupCellDegrees &&
				box[0] <= float64(cell.row)*pickupCellDegrees && box[2] >= float64(cell.row+1)*pickupCellDegrees
			for _, region := range regions {
				bucket := cell.regions[region]
				if len(bucket) == 0 {
					continue
				}
				if full {
					result.Total += len(bucket)
					count := min(len(bucket), PickupMapLimit-len(result.Items))
					for _, position := range bucket[:count] {
						result.Items = append(result.Items, i.points[position])
					}
				} else {
					for _, position := range bucket {
						point := &i.points[position]
						if box.contains(point.Location) {
							result.Total++
							if len(result.Items) < PickupMapLimit {
								result.Items = append(result.Items, *point)
							}
						}
					}
				}
			}
		}
	}
	result.Refine = result.Total > PickupMapLimit
	return result
}

// RegionBounds возвращает охват только разрешённых регионов.
func (i *PickupIndex) RegionBounds(regions []string) *Bounds {
	var result *Bounds
	for _, region := range regions {
		b, ok := i.bounds[region]
		if !ok {
			continue
		}
		if result == nil {
			value := b
			result = &value
		}
		result[0], result[1] = min(result[0], b[0]), min(result[1], b[1])
		result[2], result[3] = max(result[2], b[2]), max(result[3], b[3])
	}
	return result
}

func pickupCellAt(coordinate float64) int {
	return int(math.Floor(coordinate / pickupCellDegrees))
}

// List пропускает целые региональные списки по длине; начальная загрузка карты тоже не сканирует город.
func (i *PickupIndex) List(regions []string, offset, limit int) PickupArea {
	result := PickupArea{Items: []PickupPoint{}, Bounds: i.RegionBounds(regions)}
	regions = slices.Compact(slices.Sorted(slices.Values(regions)))
	for _, region := range regions {
		positions := i.regions[region]
		result.Total += len(positions)
		if offset >= len(positions) {
			offset -= len(positions)
			continue
		}
		if len(result.Items) < limit {
			count := min(len(positions)-offset, limit-len(result.Items))
			for _, position := range positions[offset : offset+count] {
				result.Items = append(result.Items, i.points[position])
			}
		}
		offset = 0
	}
	return result
}
