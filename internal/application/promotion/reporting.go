package promotion

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/promotion"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// Calendar возвращает занятость по дням и позициям.
type Calendar struct {
	Code string        `json:"code"`
	Days []CalendarDay `json:"days"`
}

// CalendarDay не задаёт общий лимит для суммы несвязанных категорий.
type CalendarDay struct {
	Day         string             `json:"day"`
	Sold        int                `json:"sold"`
	Limit       *int               `json:"limit"`
	Positions   []CalendarPosition `json:"positions"`
	CampaignIDs []ids.UUID         `json:"campaignIds"`
}

// CalendarPosition показывает заполнение одной позиции.
type CalendarPosition struct {
	Position int  `json:"position"`
	Sold     int  `json:"sold"`
	Limit    *int `json:"limit"`
}

// Fill считает проданные и доступные слот-недели.
type Fill struct {
	Sold  int `json:"sold"`
	Total int `json:"total"`
}

// PlacementFill сводит заполнение одного места.
type PlacementFill struct {
	Code  string `json:"code"`
	Title string `json:"title"`
	Sold  int    `json:"sold"`
	Total int    `json:"total"`
}

// RevenueDay хранит целые копейки дневной доли цены.
type RevenueDay struct {
	Day     string `json:"day"`
	Kopecks int64  `json:"kopecks"`
}

// TrafficDay суммирует показы и клики всех размещений.
type TrafficDay struct {
	Day         string `json:"day"`
	Impressions int64  `json:"impressions"`
	Clicks      int64  `json:"clicks"`
}

// Overview сводит выручку, занятость и трафик за период.
type Overview struct {
	Period          promotion.Period `json:"period"`
	RevenueKopecks  int64            `json:"revenueKopecks"`
	ActiveCampaigns int              `json:"activeCampaigns"`
	TotalCampaigns  int              `json:"totalCampaigns"`
	Fill            Fill             `json:"fill"`
	Placements      []PlacementFill  `json:"placements"`
	ReviewQueue     int              `json:"reviewQueue"`
	RevenueByDay    []RevenueDay     `json:"revenueByDay"`
	TrafficByDay    []TrafficDay     `json:"trafficByDay"`
}

func reportDays(from, to string, location *time.Location) ([]time.Time, error) {
	first, err := time.ParseInLocation(time.DateOnly, from, location)
	if err != nil {
		return nil, promotion.ErrInvalid
	}
	last, err := time.ParseInLocation(time.DateOnly, to, location)
	if err != nil || last.Before(first) {
		return nil, promotion.ErrInvalid
	}
	result := []time.Time{}
	for day := first; !day.After(last); day = day.AddDate(0, 0, 1) {
		if len(result) == 3660 {
			return nil, promotion.ErrInvalid
		}
		result = append(result, day)
	}
	return result, nil
}

// Calendar возвращает занятость по дням и позициям.
func (s *Service) Calendar(ctx context.Context, code, from, to, category string) (Calendar, error) {
	result := Calendar{Code: code, Days: []CalendarDay{}}
	dates, err := reportDays(from, to, s.config.Location)
	if err != nil {
		return result, err
	}
	err = s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		p, err := tx.Promotion().Placement(ctx, code)
		if err != nil {
			return err
		}
		all, _, err := tx.Promotion().List(ctx, promotion.Filter{PlacementCode: code, From: from, To: to})
		if err != nil {
			return err
		}
		for _, date := range dates {
			day := CalendarDay{
				Day:         date.Format(time.DateOnly),
				CampaignIDs: []ids.UUID{},
				Positions:   []CalendarPosition{},
			}
			limit := p.State().SlotLimit
			if p.State().Scope == promotion.Global || category != "" {
				day.Limit = &limit
			}
			for _, position := range p.State().Positions {
				row := CalendarPosition{Position: position, Limit: day.Limit}
				for _, c := range all {
					if !promotion.Occupies(c.Status) || c.Period.From > day.Day || c.Period.To < day.Day ||
						c.Position != position || (category != "" && c.Target.CategoryID != category) {
						continue
					}
					row.Sold++
					day.Sold++
					day.CampaignIDs = append(day.CampaignIDs, c.ID)
				}
				day.Positions = append(day.Positions, row)
			}
			if day.Limit != nil {
				total := limit * len(p.State().Positions)
				day.Limit = &total
			}
			result.Days = append(result.Days, day)
		}
		return nil
	})
	return result, err
}

// Overview сводит выручку, занятость и трафик за период.
func (s *Service) Overview(ctx context.Context, from, to string) (Overview, error) {
	result := Overview{
		Period:       promotion.Period{From: from, To: to},
		Placements:   []PlacementFill{},
		RevenueByDay: []RevenueDay{},
		TrafficByDay: []TrafficDay{},
	}
	dates, err := reportDays(from, to, s.config.Location)
	if err != nil {
		return result, err
	}
	err = s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		all, _, err := tx.Promotion().List(ctx, promotion.Filter{From: from, To: to})
		if err != nil {
			return err
		}
		placements, err := tx.Promotion().Placements(ctx)
		if err != nil {
			return err
		}
		revenue := map[string]money.Money{}
		traffic := map[string]TrafficDay{}
		result.TotalCampaigns = len(all)
		for _, c := range all {
			if c.Status == promotion.Active {
				result.ActiveCampaigns++
			}
			if c.Status == promotion.Review {
				result.ReviewQueue++
			}
			if c.Status == promotion.Active || c.Status == promotion.Finished {
				days, err := reportDays(c.Period.From, c.Period.To, s.config.Location)
				if err != nil {
					return err
				}
				for i, day := range days {
					key := day.Format(time.DateOnly)
					if key < from || key > to {
						continue
					}
					value := c.Price.Kopecks() / int64(len(days))
					if int64(i) < c.Price.Kopecks()%int64(len(days)) {
						value++
					}
					revenue[key], err = revenue[key].Add(money.FromKopecks(value))
					if err != nil {
						return err
					}
				}
			}
			rows, err := tx.Promotion().Stats(ctx, c.ID, from, to)
			if err != nil {
				return err
			}
			for _, row := range rows {
				day := traffic[row.Day]
				day.Day = row.Day
				day.Impressions += row.Impressions
				day.Clicks += row.Clicks
				traffic[row.Day] = day
			}
		}
		total := money.Money{}
		for _, date := range dates {
			day := date.Format(time.DateOnly)
			total, err = total.Add(revenue[day])
			if err != nil {
				return err
			}
			result.RevenueByDay = append(result.RevenueByDay, RevenueDay{Day: day, Kopecks: revenue[day].Kopecks()})
			row := traffic[day]
			row.Day = day
			result.TrafficByDay = append(result.TrafficByDay, row)
		}
		result.RevenueKopecks = total.Kopecks()
		for _, p := range placements {
			row := placementFill(p, all, dates)
			result.Placements = append(result.Placements, row)
			result.Fill.Sold += row.Sold
			result.Fill.Total += row.Total
		}
		return nil
	})
	return result, err
}

func placementFill(p promotion.PlacementState, all []promotion.CampaignState, dates []time.Time) PlacementFill {
	result := PlacementFill{Code: p.Code, Title: p.Title}
	weeks := map[string]bool{}
	categories := map[string]bool{}
	for _, day := range dates {
		monday := day.AddDate(0, 0, -(int(day.Weekday())+6)%7).Format(time.DateOnly)
		weeks[monday] = true
	}
	for _, c := range all {
		if c.PlacementCode != p.Code {
			continue
		}
		categories[c.Target.CategoryID] = true
		if !promotion.Occupies(c.Status) {
			continue
		}
		for week := range weeks {
			if c.Period.From <= week && c.Period.To >= week {
				result.Sold++
			}
		}
	}
	count := 1
	if p.Scope == promotion.Category {
		count = len(categories)
	}
	result.Total = len(weeks) * len(p.Positions) * p.SlotLimit * count
	return result
}
