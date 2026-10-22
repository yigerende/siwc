package transport

import (
	"net/http"
	"sort"
	"sync"
	"time"
)

// Upstream connections are grouped per account (and proxy) so accounts never
// share a connection. Each lane owns a dedicated transport, i.e. its own
// HTTP/2 connection, and carries at most maxStreams concurrent requests; a new
// lane is opened when every lane of the account is full. The number of lanes
// is not capped: this bounds how many requests one connection reset can take
// down, not how many requests an account can run.
const (
	upstreamLaneIdleTTL   = 2 * time.Minute
	upstreamPoolIdleTTL   = 10 * time.Minute
	upstreamSweepInterval = 30 * time.Second
)

type upstreamPoolKey struct {
	accountID int64
	proxyURL  string
}

type upstreamLane struct {
	client   *http.Client
	inFlight int
	lastUsed time.Time
}

type upstreamPool struct {
	lanes    []*upstreamLane
	lastUsed time.Time
}

type upstreamPools struct {
	mu        sync.Mutex
	pools     map[upstreamPoolKey]*upstreamPool
	lastSweep time.Time
}

// acquire returns a lane of the key's pool with spare stream capacity, opening
// a new lane through newClient when all existing lanes are full. The returned
// release must be called once the request's response body is closed (or the
// request failed); extra calls are ignored.
func (pools *upstreamPools) acquire(key upstreamPoolKey, maxStreams int, newClient func() (*http.Client, error)) (*http.Client, func(), error) {
	if maxStreams < 1 {
		maxStreams = 1
	}
	now := time.Now()

	pools.mu.Lock()
	defer pools.mu.Unlock()
	if pools.pools == nil {
		pools.pools = make(map[upstreamPoolKey]*upstreamPool)
	}
	if now.Sub(pools.lastSweep) >= upstreamSweepInterval {
		pools.sweepLocked(now)
		pools.lastSweep = now
	}
	pool := pools.pools[key]
	if pool == nil {
		pool = &upstreamPool{}
		pools.pools[key] = pool
	}
	var lane *upstreamLane
	for _, candidate := range pool.lanes {
		if candidate.inFlight < maxStreams {
			lane = candidate
			break
		}
	}
	if lane == nil {
		client, err := newClient()
		if err != nil {
			return nil, nil, err
		}
		lane = &upstreamLane{client: client}
		pool.lanes = append(pool.lanes, lane)
	}
	lane.inFlight++
	lane.lastUsed = now
	pool.lastUsed = now

	var once sync.Once
	release := func() {
		once.Do(func() {
			pools.mu.Lock()
			defer pools.mu.Unlock()
			lane.inFlight--
			lane.lastUsed = time.Now()
			pool.lastUsed = lane.lastUsed
		})
	}
	return lane.client, release, nil
}

// sweepLocked closes lanes that have been idle for upstreamLaneIdleTTL and
// forgets pools of accounts unused for upstreamPoolIdleTTL (SIWC accounts are
// replaced frequently, so stale pools would otherwise pile up).
func (pools *upstreamPools) sweepLocked(now time.Time) {
	for key, pool := range pools.pools {
		kept := pool.lanes[:0]
		for _, lane := range pool.lanes {
			if lane.inFlight == 0 && now.Sub(lane.lastUsed) >= upstreamLaneIdleTTL {
				closeLaneIdleConnections(lane)
				continue
			}
			kept = append(kept, lane)
		}
		for index := len(kept); index < len(pool.lanes); index++ {
			pool.lanes[index] = nil
		}
		pool.lanes = kept
		if len(pool.lanes) == 0 && now.Sub(pool.lastUsed) >= upstreamPoolIdleTTL {
			delete(pools.pools, key)
		}
	}
}

// reset forgets every pool so new requests open fresh lanes (used after a
// config change). Lanes still carrying requests keep running until their
// responses finish; nothing reuses them afterwards.
func (pools *upstreamPools) reset() {
	pools.mu.Lock()
	defer pools.mu.Unlock()
	for _, pool := range pools.pools {
		for _, lane := range pool.lanes {
			closeLaneIdleConnections(lane)
		}
	}
	pools.pools = make(map[upstreamPoolKey]*upstreamPool)
}

func (pools *upstreamPools) closeIdleConnections() {
	pools.mu.Lock()
	defer pools.mu.Unlock()
	for _, pool := range pools.pools {
		for _, lane := range pool.lanes {
			closeLaneIdleConnections(lane)
		}
	}
}

func closeLaneIdleConnections(lane *upstreamLane) {
	if transport, ok := lane.client.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
}

// UpstreamAccountStats describes one account's upstream connections.
type UpstreamAccountStats struct {
	AccountID   int64 `json:"account_id"`
	Connections int   `json:"connections"`
	InFlight    int   `json:"in_flight"`
}

// UpstreamStats summarizes the per-account upstream connection pools.
type UpstreamStats struct {
	Accounts    int                    `json:"accounts"`
	Connections int                    `json:"connections"`
	InFlight    int                    `json:"in_flight"`
	PerAccount  []UpstreamAccountStats `json:"per_account"`
}

func (pools *upstreamPools) stats() UpstreamStats {
	pools.mu.Lock()
	defer pools.mu.Unlock()
	byAccount := make(map[int64]*UpstreamAccountStats)
	stats := UpstreamStats{}
	for key, pool := range pools.pools {
		entry := byAccount[key.accountID]
		if entry == nil {
			entry = &UpstreamAccountStats{AccountID: key.accountID}
			byAccount[key.accountID] = entry
		}
		for _, lane := range pool.lanes {
			entry.Connections++
			entry.InFlight += lane.inFlight
		}
	}
	for _, entry := range byAccount {
		stats.Connections += entry.Connections
		stats.InFlight += entry.InFlight
		stats.PerAccount = append(stats.PerAccount, *entry)
	}
	sort.Slice(stats.PerAccount, func(i, j int) bool { return stats.PerAccount[i].AccountID < stats.PerAccount[j].AccountID })
	stats.Accounts = len(stats.PerAccount)
	return stats
}
