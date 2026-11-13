package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type markerAccountRepo struct {
	AccountRepository
	account *Account
	saved   int
	reads   [][]int64
	result  map[int64]*SpaceWorkflow
	err     error
}

func (r *markerAccountRepo) GetByID(context.Context, int64) (*Account, error) { return r.account, nil }
func (r *markerAccountRepo) SaveSpaceWorkflow(_ context.Context, _ int64, m SpaceWorkflow) (*SpaceWorkflowReceipt, error) {
	r.saved++
	return &SpaceWorkflowReceipt{Revision: m.Revision, Applied: true}, nil
}
func (r *markerAccountRepo) SpaceWorkflows(_ context.Context, ids []int64) (map[int64]*SpaceWorkflow, error) {
	r.reads = append(r.reads, append([]int64(nil), ids...))
	return r.result, r.err
}
func validSpaceMarker() SpaceWorkflow {
	return SpaceWorkflow{SourceID: "0123456789abcdef0123456789abcdef", Revision: 1, Email: "a@example.com", FlowID: "flow", Stage: "merge", Completed: true, ChangedAt: time.Now().UTC().Truncate(time.Millisecond)}
}

func TestSpaceWorkflowValidationAndIdentity(t *testing.T) {
	valid := validSpaceMarker()
	for _, change := range []func(*SpaceWorkflow){func(v *SpaceWorkflow) { v.SourceID = "bad" }, func(v *SpaceWorkflow) { v.Revision = 0 }, func(v *SpaceWorkflow) { v.Email = "broken" }, func(v *SpaceWorkflow) { v.Stage = "quota" }, func(v *SpaceWorkflow) { v.FlowID = "" }, func(v *SpaceWorkflow) { v.ChangedAt = time.Time{} }, func(v *SpaceWorkflow) { v.ChangedAt = time.Now().Add(time.Hour) }} {
		bad := valid
		change(&bad)
		require.Error(t, bad.Validate())
	}
	for _, stage := range []string{"merge", "merge_v2", "merchant", "deactivate"} {
		v := valid
		v.Stage = stage
		require.NoError(t, v.Validate())
	}
	for _, auth := range []string{"siwc", "oauth", ""} {
		for _, email := range []string{"A@example.com", "someone@example.com"} {
			repo := &markerAccountRepo{account: &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"auth_mode": auth, "email": email}}}
			svc := &adminServiceImpl{accountRepo: repo}
			_, err := svc.SaveSpaceWorkflow(context.Background(), 7, valid)
			if auth == "siwc" && email == "A@example.com" {
				require.NoError(t, err)
				require.Equal(t, 1, repo.saved)
			} else {
				require.ErrorIs(t, err, ErrSpaceWorkflowConflict)
				require.Zero(t, repo.saved)
			}
		}
	}
}

func TestSpaceWorkflowListBatchesOnlyCurrentSIWCPage(t *testing.T) {
	marker := validSpaceMarker()
	repo := &markerAccountRepo{result: map[int64]*SpaceWorkflow{1: &marker}}
	svc := &adminServiceImpl{accountRepo: repo}
	rows := []Account{{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"auth_mode": "siwc"}}, {ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth}, {ID: 3, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"openai_auth_mode": "siwc"}}}
	require.NoError(t, svc.loadSpaceWorkflows(context.Background(), rows))
	require.Equal(t, [][]int64{{1, 3}}, repo.reads)
	require.Equal(t, &marker, rows[0].SpaceWorkflow)
	require.Nil(t, rows[1].SpaceWorkflow)
	require.Nil(t, rows[2].SpaceWorkflow)
	repo.err = errors.New("database unavailable")
	require.Error(t, svc.loadSpaceWorkflows(context.Background(), rows))
}
