package platformapi

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	reviewCacheCapacity = 512
	reviewCacheTTL      = 30 * time.Second
)

var (
	// ErrReviewsNotFound — сервер не нашёл товар для публичных отзывов.
	ErrReviewsNotFound = errs.New(errs.NotFound, "platform.reviews_not_found", "товар для отзывов не найден")
	// ErrReviewsUnavailable — отказ чтения отзывов, независимый от словаря заказов.
	ErrReviewsUnavailable = errs.New(errs.Unavailable, "platform.reviews_unavailable", "отзывы временно недоступны")
)

type reviewCacheEntry struct {
	page    ports.ReviewPage
	expires time.Time
}

// ProductReviews читает только опубликованные отзывы по доверенному каналу.
func (c *Client) ProductReviews(
	ctx context.Context,
	tenant ids.TenantID,
	product ids.ProductID,
	limit, offset int,
) (ports.ReviewPage, error) {
	result := ports.ReviewPage{Items: []ports.Review{}}
	path := "/products/" + url.PathEscape(string(product)) + "/reviews?limit=" + strconv.Itoa(limit)
	path += "&offset=" + strconv.Itoa(offset)
	key := string(tenant) + path
	value, err, _ := c.reviewsFlight.Do(key, func() (any, error) {
		c.reviewsMu.Lock()
		entry, found := c.reviews[key]
		c.reviewsMu.Unlock()
		if found && time.Now().Before(entry.expires) {
			return entry.page, nil
		}
		page, err := c.loadReviews(context.WithoutCancel(ctx), tenant, path, limit)
		if err != nil {
			return ports.ReviewPage{}, err
		}
		c.reviewsMu.Lock()
		if len(c.reviews) >= reviewCacheCapacity {
			c.reviews = nil
		}
		if c.reviews == nil {
			c.reviews = make(map[string]reviewCacheEntry)
		}
		c.reviews[key] = reviewCacheEntry{page: page, expires: time.Now().Add(reviewCacheTTL)}
		c.reviewsMu.Unlock()
		return page, nil
	})
	if err != nil {
		return result, err
	}
	result = value.(ports.ReviewPage)
	result.Items = append([]ports.Review{}, result.Items...)
	return result, nil
}

func (c *Client) loadReviews(
	ctx context.Context,
	tenant ids.TenantID,
	path string,
	limit int,
) (ports.ReviewPage, error) {
	result := ports.ReviewPage{Items: []ports.Review{}}
	if err := c.callWithRefusal(ctx, http.MethodGet, tenant, path, nil, &result, reviewRefusal); err != nil {
		return ports.ReviewPage{}, err
	}
	if result.Total < 0 || len(result.Items) > limit || len(result.Items) > result.Total {
		return ports.ReviewPage{}, ErrInvalidResponse
	}
	for _, review := range result.Items {
		if review.Rating < 1 || review.Rating > 5 || review.CreatedAt.IsZero() {
			return ports.ReviewPage{}, ErrInvalidResponse
		}
	}
	if result.Items == nil {
		result.Items = []ports.Review{}
	}
	return result, nil
}

func reviewRefusal(_, _ string, tenant ids.TenantID, reply *http.Response) error {
	status := reply.StatusCode
	slog.Warn("сервер отказал в чтении отзывов", "tenant", tenant, "status", status)
	if status == http.StatusNotFound {
		return ErrReviewsNotFound
	}
	return ErrReviewsUnavailable
}
