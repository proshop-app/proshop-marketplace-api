package promotion

import "time"

// Period задаёт включительные границы календарных недель.
type Period struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Week передаёт занятость недели без зависимости домена от хранилища.
type Week struct {
	From string `json:"from"`
	Sold int    `json:"sold"`
}

// NewPeriod использует календарь: переход летнего времени не меняет границы недели.
func NewPeriod(from string, weeks int, location *time.Location) (Period, error) {
	if location == nil || weeks < 1 || weeks > 520 {
		return Period{}, ErrInvalid
	}
	start, err := time.ParseInLocation(time.DateOnly, from, location)
	if err != nil || start.Weekday() != time.Monday {
		return Period{}, ErrInvalid
	}
	return Period{From: from, To: start.AddDate(0, 0, weeks*7-1).Format(time.DateOnly)}, nil
}

// Weeks проверяет календарные границы и перечисляет понедельники.
func (p Period) Weeks(location *time.Location) ([]string, error) {
	if location == nil {
		return nil, ErrInvalid
	}
	start, err := time.ParseInLocation(time.DateOnly, p.From, location)
	if err != nil || start.Weekday() != time.Monday {
		return nil, ErrInvalid
	}
	end, err := time.ParseInLocation(time.DateOnly, p.To, location)
	if err != nil || end.Weekday() != time.Sunday || end.Before(start) {
		return nil, ErrInvalid
	}
	result := []string{}
	for day := start; !day.After(end); day = day.AddDate(0, 0, 7) {
		if len(result) == 520 {
			return nil, ErrInvalid
		}
		result = append(result, day.Format(time.DateOnly))
	}
	return result, nil
}

// ValidateStart не допускает покупку прошедших недель.
func (p Period) ValidateStart(now time.Time, location *time.Location) error {
	if _, err := p.Weeks(location); err != nil {
		return err
	}
	day := now.In(location)
	distance := (8 - int(day.Weekday())) % 7
	monday := day.AddDate(0, 0, distance).Format(time.DateOnly)
	if p.From < monday {
		return ErrInvalid
	}
	return nil
}

// CheckCapacity получает занятость без самой редактируемой кампании.
func CheckCapacity(weeks []Week, limit int) error {
	if limit < 1 {
		return ErrInvalid
	}
	occupied := []string{}
	for _, week := range weeks {
		if week.Sold < 0 {
			return ErrInvalid
		}
		if week.Sold >= limit {
			occupied = append(occupied, week.From)
		}
	}
	if len(occupied) > 0 {
		return &CapacityError{Weeks: occupied}
	}
	return nil
}
