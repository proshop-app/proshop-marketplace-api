package promotion

import (
	"github.com/proshop-app/proshop-marketplace/internal/domain/promotion"
)

// Period задаёт включительные границы календарных недель.
func (s *Service) Period(from string, weeks int) (promotion.Period, error) {
	return promotion.NewPeriod(from, weeks, s.config.Location)
}

// Weeks проверяет календарные границы и перечисляет понедельники.
func (s *Service) Weeks(period promotion.Period) (int, error) {
	weeks, err := period.Weeks(s.config.Location)
	return len(weeks), err
}
