package repository

import (
	"context"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// Only per-second cumulative counts are stored, never individual request IDs.
// Redis time anchors each bucket's age so instances need not share wall clocks.
const writeAccountRequestRPMScript = `
redis.replicate_commands()
local now = tonumber(redis.call('TIME')[1])
local expired = redis.call('ZRANGEBYSCORE', KEYS[2], '-inf', now - 60)
for _, field in ipairs(expired) do redis.call('HDEL', KEYS[1], field) end
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', now - 60)
for i = 3, #ARGV, 2 do
    local age = math.max(0, tonumber(ARGV[2]) - tonumber(ARGV[i]))
    if age < 60 then
        local field = ARGV[1] .. ':' .. ARGV[i]
        local count = tonumber(ARGV[i + 1])
        local previous = tonumber(redis.call('HGET', KEYS[1], field) or '0')
        if count > previous then redis.call('HSET', KEYS[1], field, count) end
        redis.call('ZADD', KEYS[2], 'NX', now - age, field)
    end
end
redis.call('EXPIRE', KEYS[1], 120)
redis.call('EXPIRE', KEYS[2], 120)
return 1
`

const readAccountRequestRPMScript = `
local now = tonumber(redis.call('TIME')[1])
local fields = redis.call('ZRANGEBYSCORE', KEYS[2], '(' .. (now - 60), '+inf')
local total = 0
for _, field in ipairs(fields) do
    total = total + tonumber(redis.call('HGET', KEYS[1], field) or '0')
end
return total
`

func accountRequestRPMKeys(accountID int64) []string {
	prefix := "account_request_rpm:{" + strconv.FormatInt(accountID, 10) + "}:"
	return []string{prefix + "counts", prefix + "buckets"}
}

func (c *concurrencyCache) WriteAccountRequestRPM(ctx context.Context, instanceID string, observedAt int64, snapshots []service.AccountRequestRPMSnapshot) error {
	if len(snapshots) == 0 {
		return nil
	}
	byAccount := make(map[int64][]any)
	for _, snapshot := range snapshots {
		if snapshot.AccountID <= 0 || snapshot.Count <= 0 {
			continue
		}
		args := byAccount[snapshot.AccountID]
		if args == nil {
			args = []any{instanceID, observedAt}
		}
		byAccount[snapshot.AccountID] = append(args, snapshot.Second, snapshot.Count)
	}
	_, err := c.rdb.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		for accountID, args := range byAccount {
			pipe.Eval(ctx, writeAccountRequestRPMScript, accountRequestRPMKeys(accountID), args...)
		}
		return nil
	})
	return err
}

func (c *concurrencyCache) GetAccountRequestRPMBatch(ctx context.Context, accountIDs []int64) (map[int64]int, error) {
	counts := make(map[int64]int, len(accountIDs))
	if len(accountIDs) == 0 {
		return counts, nil
	}
	commands := make(map[int64]*redis.Cmd, len(accountIDs))
	_, err := c.rdb.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		for _, accountID := range accountIDs {
			if accountID > 0 && commands[accountID] == nil {
				commands[accountID] = pipe.Eval(ctx, readAccountRequestRPMScript, accountRequestRPMKeys(accountID))
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for accountID, cmd := range commands {
		count, err := cmd.Int64()
		if err != nil {
			return nil, err
		}
		counts[accountID] = int(count)
	}
	return counts, nil
}

var _ service.AccountRequestRPMCache = (*concurrencyCache)(nil)
