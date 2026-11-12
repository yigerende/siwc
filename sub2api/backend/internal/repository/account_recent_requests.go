package repository

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func accountRecentRequestsQuery(input service.AccountRecentRequestsInput) (string, []any) {
	values := make([]string, len(input.AccountIDs))
	args := make([]any, len(input.AccountIDs))
	for i, id := range input.AccountIDs {
		values[i], args[i] = fmt.Sprintf("($%d::bigint)", i+1), id
	}
	// Both sources use their account/time indexes. Only bounded summaries are
	// read, never request/response bodies, credentials, or full error histories.
	query := `SELECT wanted.account_id,recent.id,recent.source,recent.created_at,recent.request_id,
 recent.failed,recent.final_failed,recent.status_code,recent.error_message
 FROM (VALUES ` + strings.Join(values, ",") + `) AS wanted(account_id)
 CROSS JOIN LATERAL (
 (SELECT id,'usage' AS source,created_at,COALESCE(request_id,'') AS request_id,
 false AS failed,false AS final_failed,0 AS status_code,'' AS error_message
 FROM usage_logs WHERE account_id=wanted.account_id
 ORDER BY created_at DESC,id DESC LIMIT 10)
 UNION ALL
 (SELECT id,'error' AS source,created_at,COALESCE(request_id,'') AS request_id,
 true AS failed,COALESCE(status_code,0)>=400 AS final_failed,
 COALESCE(NULLIF(upstream_status_code,0),NULLIF(status_code,0),0) AS status_code,
 LEFT(COALESCE(NULLIF(upstream_error_message,''),NULLIF(error_message,''),NULLIF(provider_error_code,''),error_type,''),500) AS error_message
 FROM ops_error_logs WHERE account_id=wanted.account_id
 ORDER BY created_at DESC,id DESC LIMIT 10)
 ) recent ORDER BY wanted.account_id,recent.created_at DESC,recent.id DESC,recent.source`
	return query, args
}

type recentAccountRequestRow struct {
	service.AccountRecentRequest
	id          int64
	source      string
	requestID   string
	finalFailed bool
}

func mergeRecentAccountRequests(rows []recentAccountRequestRow) []service.AccountRecentRequest {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].CreatedAt.Equal(rows[j].CreatedAt) {
			return rows[i].id > rows[j].id
		}
		return rows[i].CreatedAt.After(rows[j].CreatedAt)
	})
	merged := make([]recentAccountRequestRow, 0, len(rows))
	indices := map[string]int{}
	for _, row := range rows {
		key := strings.TrimSpace(row.requestID)
		if key == "" {
			key = fmt.Sprintf("%s:%d", row.source, row.id)
		} else {
			key = "request:" + key
		}
		if i, ok := indices[key]; ok {
			previous := &merged[i]
			// Recovered upstream errors and successful usage can refer to the
			// same request. Show one green bar with its diagnostic, unless a
			// terminal failure (including a streaming failure) was recorded.
			previous.finalFailed = previous.finalFailed || row.finalFailed
			previous.Failed = previous.finalFailed || (previous.Failed && row.Failed)
			if previous.ErrorMessage == "" && row.ErrorMessage != "" {
				previous.ErrorMessage, previous.StatusCode = row.ErrorMessage, row.StatusCode
			}
			continue
		}
		indices[key] = len(merged)
		merged = append(merged, row)
	}
	out := make([]service.AccountRecentRequest, 0, service.AccountRecentRequestLimit)
	for _, row := range merged {
		out = append(out, row.AccountRecentRequest)
		if len(out) == service.AccountRecentRequestLimit {
			break
		}
	}
	return out
}

func (r *usageLogRepository) LatestAccountRequests(ctx context.Context, input service.AccountRecentRequestsInput) ([]service.AccountRecentRequests, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	query, args := accountRecentRequestsQuery(input)
	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byAccount := make(map[int64][]recentAccountRequestRow, len(input.AccountIDs))
	for rows.Next() {
		var accountID int64
		var row recentAccountRequestRow
		if err := rows.Scan(&accountID, &row.id, &row.source, &row.CreatedAt, &row.requestID, &row.Failed, &row.finalFailed, &row.StatusCode, &row.ErrorMessage); err != nil {
			return nil, err
		}
		byAccount[accountID] = append(byAccount[accountID], row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]service.AccountRecentRequests, 0, len(input.AccountIDs))
	for _, id := range input.AccountIDs {
		out = append(out, service.AccountRecentRequests{AccountID: id, Requests: mergeRecentAccountRequests(byAccount[id])})
	}
	return out, nil
}
