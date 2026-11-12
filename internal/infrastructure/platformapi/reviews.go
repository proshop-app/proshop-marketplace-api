package platformapi

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

var (
	// ErrReviewsNotFound — сервер не нашёл товар для публичных отзывов.
	ErrReviewsNotFound = errs.New(errs.NotFound, "platform.reviews_not_found", "товар для отзывов не найден")
	// ErrReviewsUnavailable — отказ чтения отзывов, независимый от словаря заказов.
	ErrReviewsUnavailable = errs.New(errs.Unavailable, "platform.reviews_unavailable", "отзывы временно недоступны")
)

// ProductReviews читает только опубликованные отзывы по доверенному каналу.
func (c *Client) ProductReviews(
	ctx context.Context,
	tenant ids.TenantID,
	product ids.ProductID,
	limit, offset int,
) (ports.ReviewPage, error) {
	path := "/products/" + url.PathEscape(string(product)) + "/reviews?limit=" + strconv.Itoa(limit)
	path += "&offset=" + strconv.Itoa(offset)
	// Кэш и совместные загрузки принадлежат обёртке порта; адаптер обязан соблюдать дедлайн блока.
	return c.loadReviews(ctx, tenant, path, limit)
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

func reviewRefusal(_, _ string, _ ids.TenantID, reply *http.Response) error {
	status := reply.StatusCode
	if status == http.StatusNotFound {
		return ErrReviewsNotFound
	}
	return ErrReviewsUnavailable
}
