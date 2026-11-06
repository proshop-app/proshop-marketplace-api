package catalog

import "math"

// Rating — средний балл и число опубликованных оценок.
type Rating struct {
	Value float64 `json:"value"`
	Count int64   `json:"count"`
}

// ProductRating не создаёт выдуманный балл для товара без оценок.
func ProductRating(value float64, count int64) *Rating {
	if count <= 0 {
		return nil
	}
	rating, err := NormalizeRating(value, count)
	if err != nil {
		return nil
	}
	return &rating
}

// NormalizeRating проверяет счётчик и приводит балл к диапазону 0–5 с одним знаком.
func NormalizeRating(value float64, count int64) (Rating, error) {
	if count < 0 || math.IsNaN(value) {
		return Rating{}, ErrInvalidSnapshot.WithMessage("некорректный рейтинг")
	}
	return Rating{Value: math.Round(math.Max(0, math.Min(5, value))*10) / 10, Count: count}, nil
}
