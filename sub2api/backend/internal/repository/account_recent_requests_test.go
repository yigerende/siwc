//go:build unit

package repository

import (
	"context"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountRecentRequestsBoundedRead(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	input := service.AccountRecentRequestsInput{AccountIDs: []int64{1, 2}}
	query, args := accountRecentRequestsQuery(input)
	require.Equal(t, []any{int64(1), int64(2)}, args)
	for _, fragment := range []string{"CROSS JOIN LATERAL", "FROM usage_logs WHERE account_id=wanted.account_id", "FROM ops_error_logs WHERE account_id=wanted.account_id", "LIMIT 10", "LEFT(COALESCE"} {
		require.Contains(t, query, fragment)
	}
	for _, forbidden := range []string{"COUNT(", "OFFSET", "SELECT *", "error_body", "request_body", "request_headers", "UPDATE ", "INSERT "} {
		require.NotContains(t, query, forbidden)
	}
	now := time.Now()
	rows := sqlmock.NewRows([]string{"account_id", "id", "source", "created_at", "request_id", "failed", "final_failed", "status_code", "error_message"}).
		AddRow(1, 30, "error", now, "request-a", true, true, 429, "Rate limit").
		AddRow(1, 20, "usage", now.Add(-time.Minute), "request-b", false, false, 0, "")
	mock.ExpectQuery(regexp.QuoteMeta(query)).WithArgs(int64(1), int64(2)).WillReturnRows(rows)
	out, err := newUsageLogRepositoryWithSQL(nil, db).LatestAccountRequests(context.Background(), input)
	require.NoError(t, err)
	require.Len(t, out, 2)
	require.Len(t, out[0].Requests, 2)
	require.True(t, out[0].Requests[0].Failed)
	require.Equal(t, 429, out[0].Requests[0].StatusCode)
	require.False(t, out[0].Requests[1].Failed)
	require.NotNil(t, out[1].Requests)
	require.Empty(t, out[1].Requests)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountRecentRequestsMergeRecoveryTerminalAndBound(t *testing.T) {
	now := time.Now()
	rows := []recentAccountRequestRow{
		{AccountRecentRequest: service.AccountRecentRequest{CreatedAt: now, Failed: true, StatusCode: 429, ErrorMessage: "Rate limit"}, source: "error", requestID: "recovered"},
		{AccountRecentRequest: service.AccountRecentRequest{CreatedAt: now.Add(-time.Second)}, source: "usage", requestID: "recovered"},
	}
	merged := mergeRecentAccountRequests(rows)
	require.Len(t, merged, 1)
	require.False(t, merged[0].Failed)
	require.Equal(t, "Rate limit", merged[0].ErrorMessage)
	rows[0].finalFailed = true
	require.True(t, mergeRecentAccountRequests(rows)[0].Failed)
	rows[0].requestID, rows[1].requestID = "", ""
	require.Len(t, mergeRecentAccountRequests(rows), 2)
	for i := 1; i <= 20; i++ {
		rows = append(rows, recentAccountRequestRow{AccountRecentRequest: service.AccountRecentRequest{CreatedAt: now.Add(time.Duration(i) * time.Minute)}, requestID: fmt.Sprint(i)})
	}
	merged = mergeRecentAccountRequests(rows)
	require.Len(t, merged, 10)
	require.Equal(t, now.Add(20*time.Minute), merged[0].CreatedAt)
	require.Equal(t, now.Add(11*time.Minute), merged[9].CreatedAt)
}

func TestAccountRecentRequestsValidation(t *testing.T) {
	for _, ids := range [][]int64{nil, {0}, {-1}, {1, 1}, make([]int64, 101)} {
		require.Error(t, (service.AccountRecentRequestsInput{AccountIDs: ids}).Validate())
	}
	ids := make([]int64, 100)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	require.NoError(t, (service.AccountRecentRequestsInput{AccountIDs: ids}).Validate())
}
