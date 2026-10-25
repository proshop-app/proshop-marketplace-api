package redis

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	goredis "github.com/redis/go-redis/v9"

	appidentity "github.com/proshop-app/proshop-marketplace/internal/application/identity"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
)

const refreshWait = 8 * time.Second

var (
	claimIdentityRefresh = goredis.NewScript(`
local raw = redis.call('GET', KEYS[1])
if not raw then return {0, ''} end
local doc = cjson.decode(raw)
if ARGV[2] ~= '' and doc.record.LocalID ~= ARGV[2] then return {0, ''} end
local now = redis.call('TIME')
local millis = tonumber(now[1]) * 1000 + math.floor(tonumber(now[2]) / 1000)
if doc.owner ~= '' then
    if not doc.claimedAt or millis - doc.claimedAt >= tonumber(ARGV[3]) then
        redis.call('DEL', KEYS[1])
        return {0, ''}
    end
    return {2, ''}
end
doc.owner = ARGV[1]
doc.claimedAt = millis
redis.call('SET', KEYS[1], cjson.encode(doc), 'KEEPTTL')
return {1, cjson.encode(doc.record)}
`)
	finishIdentityRefresh = goredis.NewScript(`
local raw = redis.call('GET', KEYS[1])
if not raw then return 0 end
local doc = cjson.decode(raw)
if doc.owner ~= ARGV[1] then return 0 end
redis.call('SET', KEYS[1], ARGV[2], 'KEEPTTL')
return 1
`)
)

// IdentityRefreshes — новый namespace; старые opaque access-сессии не принимаются.
type IdentityRefreshes struct {
	client *goredis.Client
	prefix string
}

type buyerRefreshJSON struct {
	Record ports.IdentityRefresh `json:"record"`
	Owner  string                `json:"owner"`
}

// NewIdentityRefreshes отделяет записи разных backend-клиентов.
func NewIdentityRefreshes(client *goredis.Client, prefix string) *IdentityRefreshes {
	return &IdentityRefreshes{
		client: client,
		prefix: prefix + "identity:refresh:",
	}
}

// Save пишет только refresh и метаданные с абсолютным TTL.
func (s *IdentityRefreshes) Save(ctx context.Context, hash string, record ports.IdentityRefresh) error {
	encoded, err := json.Marshal(buyerRefreshJSON{Record: record})
	if err != nil {
		return ErrUnavailable
	}

	args := goredis.SetArgs{
		Mode:     "NX",
		ExpireAt: record.ExpiresAt,
	}
	if err := s.client.SetArgs(ctx, s.prefix+hash, encoded, args).Err(); err != nil {
		return ErrUnavailable
	}

	return nil
}

// Claim ждёт успешную ротацию; зависший обмен закрывает, не повторяя старый refresh.
func (s *IdentityRefreshes) Claim(
	ctx context.Context,
	hash, owner string,
	expected string,
) (ports.IdentityRefresh, error) {
	deadline := time.NewTimer(refreshWait)
	defer deadline.Stop()

	for {
		result, err := claimIdentityRefresh.Run(
			ctx,
			s.client,
			[]string{s.prefix + hash},
			owner,
			expected,
			refreshWait.Milliseconds(),
		).Slice()
		if err != nil || len(result) != 2 {
			if ctx.Err() != nil {
				return ports.IdentityRefresh{}, ctx.Err()
			}

			return ports.IdentityRefresh{}, ErrUnavailable
		}

		status, ok := result[0].(int64)
		if !ok {
			return ports.IdentityRefresh{}, ErrUnavailable
		}
		if status == 0 {
			return ports.IdentityRefresh{}, buyer.ErrSessionNotFound
		}
		if status == 1 {
			var record ports.IdentityRefresh
			raw, ok := result[1].(string)
			if !ok || json.Unmarshal([]byte(raw), &record) != nil {
				return ports.IdentityRefresh{}, ErrUnavailable
			}

			return record, nil
		}

		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()

			return ports.IdentityRefresh{}, ctx.Err()
		case <-deadline.C:
			timer.Stop()

			return ports.IdentityRefresh{}, appidentity.ErrRefreshBusy
		case <-timer.C:
		}
	}
}

// Finish — CAS без продления TTL; logout/удаление не воскресит запись.
func (s *IdentityRefreshes) Finish(ctx context.Context, hash, owner string, record ports.IdentityRefresh) error {
	encoded, err := json.Marshal(buyerRefreshJSON{Record: record})
	if err != nil {
		return ErrUnavailable
	}

	result, err := finishIdentityRefresh.Run(ctx, s.client, []string{s.prefix + hash}, owner, string(encoded)).Int()
	if err != nil {
		return ErrUnavailable
	}
	if result != 1 {
		return buyer.ErrSessionNotFound
	}

	return nil
}

// Delete возвращает текущий refresh для отзыва и немедленно закрывает CAS.
func (s *IdentityRefreshes) Delete(ctx context.Context, hash string) (ports.IdentityRefresh, error) {
	raw, err := s.client.GetDel(ctx, s.prefix+hash).Bytes()
	if errors.Is(err, goredis.Nil) {
		return ports.IdentityRefresh{}, buyer.ErrSessionNotFound
	}
	if err != nil {
		return ports.IdentityRefresh{}, ErrUnavailable
	}

	var doc buyerRefreshJSON
	if json.Unmarshal(raw, &doc) != nil {
		return ports.IdentityRefresh{}, ErrUnavailable
	}

	return doc.Record, nil
}
