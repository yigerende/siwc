//go:build unit

package repository

import (
	"context"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestSIWCWindowStatsUsesBothTimeBounds(t *testing.T) {
	db, mock := newSQLMock(t)
	r := &usageLogRepository{sql: db}
	end := time.Now()
	start := end.Add(-time.Hour)
	mock.ExpectQuery(`(?s)COALESCE\(account_stats_cost, total_cost\).*account_id = \$1 AND created_at >= \$2.*AND created_at < \$3`).WithArgs(int64(8), start, end).WillReturnRows(sqlmock.NewRows([]string{"requests", "tokens", "cost", "standard_cost", "user_cost"}).AddRow(3, 1200, 1.25, 2.5, 3.75))
	out, err := r.GetAccountWindowStatsRange(context.Background(), 8, start, end)
	require.NoError(t, err)
	require.Equal(t, 1.25, out.Cost)
	require.Equal(t, 3.75, out.UserCost)
	require.NoError(t, mock.ExpectationsWereMet())
}
