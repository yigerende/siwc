package service

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

const accountRequestRPMWindow = 60

// AccountRequestRPMSnapshot is cumulative per process, account and second.
// Retrying a flush must replace the snapshot, never increment it again.
type AccountRequestRPMSnapshot struct {
	AccountID int64
	Second    int64
	Count     int64
}

// AccountRequestRPMCache is display telemetry, independent of RPM admission limits.
type AccountRequestRPMCache interface {
	WriteAccountRequestRPM(ctx context.Context, instanceID string, observedAt int64, snapshots []AccountRequestRPMSnapshot) error
	GetAccountRequestRPMBatch(ctx context.Context, accountIDs []int64) (map[int64]int, error)
}

type accountRequestRPMBucket struct {
	second  int64
	count   int64
	flushed int64
}

type accountRequestRPMCounters struct {
	lastSeen int64
	buckets  [accountRequestRPMWindow]accountRequestRPMBucket
}

type accountRequestRPM struct {
	cache      AccountRequestRPMCache
	instanceID string
	now        func() time.Time
	mu         sync.Mutex
	accounts   map[int64]*accountRequestRPMCounters
	flushMu    sync.Mutex
	startOnce  sync.Once
	stopOnce   sync.Once
	stop       chan struct{}
	done       chan struct{}
}

func newAccountRequestRPM(cache AccountRequestRPMCache) *accountRequestRPM {
	return &accountRequestRPM{
		cache: cache, instanceID: generateRequestID(), now: time.Now,
		accounts: make(map[int64]*accountRequestRPMCounters),
		stop:     make(chan struct{}), done: make(chan struct{}),
	}
}

func (r *accountRequestRPM) record(accountID int64) {
	if r == nil || accountID <= 0 {
		return
	}
	second := r.now().Unix()
	r.mu.Lock()
	counters := r.accounts[accountID]
	if counters == nil {
		counters = &accountRequestRPMCounters{}
		r.accounts[accountID] = counters
	}
	counters.lastSeen = second
	bucket := &counters.buckets[second%accountRequestRPMWindow]
	if bucket.second != second {
		*bucket = accountRequestRPMBucket{second: second}
	}
	bucket.count++
	r.mu.Unlock()
}

func (r *accountRequestRPM) flush(ctx context.Context) error {
	r.flushMu.Lock()
	defer r.flushMu.Unlock()
	now := r.now().Unix()
	var snapshots []AccountRequestRPMSnapshot
	r.mu.Lock()
	for accountID, counters := range r.accounts {
		if counters.lastSeen <= now-accountRequestRPMWindow {
			delete(r.accounts, accountID)
			continue
		}
		for _, bucket := range counters.buckets {
			if bucket.second > now-accountRequestRPMWindow && bucket.count > bucket.flushed {
				snapshots = append(snapshots, AccountRequestRPMSnapshot{accountID, bucket.second, bucket.count})
			}
		}
	}
	r.mu.Unlock()
	if len(snapshots) == 0 {
		return nil
	}
	if err := r.cache.WriteAccountRequestRPM(ctx, r.instanceID, now, snapshots); err != nil {
		return err
	}
	r.mu.Lock()
	for _, snapshot := range snapshots {
		if counters := r.accounts[snapshot.AccountID]; counters != nil {
			bucket := &counters.buckets[snapshot.Second%accountRequestRPMWindow]
			if bucket.second == snapshot.Second {
				bucket.flushed = snapshot.Count
			}
		}
	}
	r.mu.Unlock()
	return nil
}

func (r *accountRequestRPM) start() {
	r.startOnce.Do(func() {
		go func() {
			defer close(r.done)
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			var lastWarning time.Time
			flush := func() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				err := r.flush(ctx)
				cancel()
				if err != nil && time.Since(lastWarning) >= 30*time.Second {
					lastWarning = time.Now()
					logger.L().Warn("account_request_rpm_flush_failed", zap.Error(err))
				}
			}
			for {
				select {
				case <-r.stop:
					flush()
					return
				case <-ticker.C:
					flush()
				}
			}
		}()
	})
}

// RecordAccountRequest does no network I/O and does not affect admission decisions.
func (s *ConcurrencyService) RecordAccountRequest(accountID int64) {
	if s != nil {
		s.requestRPM.record(accountID)
	}
}

func (s *ConcurrencyService) StartRequestRPMWorker() {
	if s != nil && s.requestRPM != nil {
		s.requestRPM.start()
	}
}

func (s *ConcurrencyService) StopRequestRPMWorker() {
	if s == nil || s.requestRPM == nil {
		return
	}
	r := s.requestRPM
	r.stopOnce.Do(func() { close(r.stop) })
	r.start() // Also permits cleanup before the worker was started.
	<-r.done
}

func (s *ConcurrencyService) GetAccountRequestRPMBatch(ctx context.Context, accountIDs []int64) (map[int64]int, error) {
	if s == nil || s.requestRPM == nil {
		return nil, errors.New("account request RPM cache unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return s.requestRPM.cache.GetAccountRequestRPMBatch(ctx, accountIDs)
}
