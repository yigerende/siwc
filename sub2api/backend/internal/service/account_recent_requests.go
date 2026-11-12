package service

import (
	"context"
	"errors"
	"time"
)

const AccountRecentRequestLimit = 10

type AccountRecentRequestsInput struct {
	AccountIDs []int64 `json:"account_ids"`
}

func (v AccountRecentRequestsInput) Validate() error {
	if len(v.AccountIDs) < 1 || len(v.AccountIDs) > 100 {
		return errors.New("require 1-100 account IDs")
	}
	seen := make(map[int64]bool, len(v.AccountIDs))
	for _, id := range v.AccountIDs {
		if id <= 0 || seen[id] {
			return errors.New("account IDs must be unique positive integers")
		}
		seen[id] = true
	}
	return nil
}

type AccountRecentRequest struct {
	CreatedAt    time.Time `json:"created_at"`
	Failed       bool      `json:"failed"`
	StatusCode   int       `json:"status_code,omitempty"`
	ErrorMessage string    `json:"error_message,omitempty"`
}

type AccountRecentRequests struct {
	AccountID int64                  `json:"account_id"`
	Requests  []AccountRecentRequest `json:"requests"`
}

type AccountRecentRequestsRepository interface {
	LatestAccountRequests(context.Context, AccountRecentRequestsInput) ([]AccountRecentRequests, error)
}

func (s *UsageService) LatestAccountRequests(ctx context.Context, input AccountRecentRequestsInput) ([]AccountRecentRequests, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	repo, ok := s.usageRepo.(AccountRecentRequestsRepository)
	if !ok {
		return nil, errors.New("account request history unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return repo.LatestAccountRequests(ctx, input)
}
