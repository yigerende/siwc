package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type requestRPMCacheStub struct {
	write func([]AccountRequestRPMSnapshot) error
}

func (s *requestRPMCacheStub) WriteAccountRequestRPM(_ context.Context, _ string, _ int64, snapshots []AccountRequestRPMSnapshot) error {
	return s.write(snapshots)
}

func (*requestRPMCacheStub) GetAccountRequestRPMBatch(context.Context, []int64) (map[int64]int, error) {
	return nil, errors.New("not used")
}

func TestAccountRequestRPMConcurrentRecording(t *testing.T) {
	var saved []AccountRequestRPMSnapshot
	r := newAccountRequestRPM(&requestRPMCacheStub{write: func(snapshots []AccountRequestRPMSnapshot) error {
		saved = snapshots
		return nil
	}})
	r.now = func() time.Time { return time.Unix(1000, 0) }
	var wg sync.WaitGroup
	for worker := range 1000 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				r.record(int64(worker%100 + 1))
			}
		}()
	}
	wg.Wait()
	require.Empty(t, saved, "recording must never perform cache I/O")
	require.NoError(t, r.flush(context.Background()))
	require.Len(t, saved, 100)
	for _, snapshot := range saved {
		require.EqualValues(t, 1000, snapshot.Count)
	}
}

func TestAccountRequestRPMFlushRetainsConcurrentIncrements(t *testing.T) {
	entered, resume, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var writes []int64
	r := newAccountRequestRPM(&requestRPMCacheStub{write: func(snapshots []AccountRequestRPMSnapshot) error {
		writes = append(writes, snapshots[0].Count)
		if len(writes) == 1 {
			close(entered)
			<-resume
		}
		return nil
	}})
	r.now = func() time.Time { return time.Unix(1000, 0) }
	r.record(1)
	go func() { done <- r.flush(context.Background()) }()
	<-entered
	for range 10 {
		r.record(1)
	}
	close(resume)
	require.NoError(t, <-done)
	require.NoError(t, r.flush(context.Background()))
	require.Equal(t, []int64{1, 11}, writes)
	require.NoError(t, r.flush(context.Background()))
	require.Len(t, writes, 2, "clean buckets need no further writes")
}

func TestAccountRequestRPMFailedFlushRetriesSnapshotAndExpires(t *testing.T) {
	now := time.Unix(1000, 0)
	var writes []AccountRequestRPMSnapshot
	fail := true
	r := newAccountRequestRPM(&requestRPMCacheStub{write: func(snapshots []AccountRequestRPMSnapshot) error {
		writes = append(writes, snapshots...)
		if fail {
			return errors.New("lost acknowledgement")
		}
		return nil
	}})
	r.now = func() time.Time { return now }
	r.record(7)
	require.Error(t, r.flush(context.Background()))
	r.record(7)
	fail = false
	require.NoError(t, r.flush(context.Background()))
	require.EqualValues(t, 1, writes[0].Count)
	require.EqualValues(t, 2, writes[1].Count)
	require.Equal(t, writes[0].Second, writes[1].Second)

	fail = true
	r.record(7)
	now = now.Add(60 * time.Second)
	require.NoError(t, r.flush(context.Background()))
	require.Empty(t, r.accounts, "expired dirty buckets must not accumulate during outages")
	require.Len(t, writes, 2)
}

func TestAccountRequestRPMShutdownFlushesPendingCounts(t *testing.T) {
	var saved []AccountRequestRPMSnapshot
	svc := &ConcurrencyService{requestRPM: newAccountRequestRPM(&requestRPMCacheStub{
		write: func(snapshots []AccountRequestRPMSnapshot) error { saved = snapshots; return nil },
	})}
	svc.RecordAccountRequest(3)
	svc.StopRequestRPMWorker()
	svc.StopRequestRPMWorker()
	require.Len(t, saved, 1)
	require.EqualValues(t, 1, saved[0].Count)
	_, err := NewConcurrencyService(nil).GetAccountRequestRPMBatch(context.Background(), []int64{3})
	require.Error(t, err)
}

func BenchmarkAccountRequestRPMRecordParallel(b *testing.B) {
	r := newAccountRequestRPM(nil)
	for id := int64(1); id <= 100; id++ {
		r.record(id)
	}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		id := int64(1)
		for pb.Next() {
			r.record(id)
			id = id%100 + 1
		}
	})
}
