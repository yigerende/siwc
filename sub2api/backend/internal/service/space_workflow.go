package service

import (
	"context"
	"errors"
	"net/mail"
	"regexp"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// Display-only metadata. It must never participate in scheduling or billing.
type SpaceWorkflow struct {
	SourceID  string    `json:"source_id"`
	Revision  int64     `json:"revision"`
	Email     string    `json:"email"`
	FlowID    string    `json:"flow_id"`
	OrderID   string    `json:"order_id,omitempty"`
	Stage     string    `json:"stage"`
	Completed bool      `json:"completed"`
	Manual    bool      `json:"manual"`
	ChangedAt time.Time `json:"changed_at"`
}

type SpaceWorkflowReceipt struct {
	Revision int64 `json:"revision"`
	Applied  bool  `json:"applied"`
}

type SpaceWorkflowRepository interface {
	SaveSpaceWorkflow(context.Context, int64, SpaceWorkflow) (*SpaceWorkflowReceipt, error)
	SpaceWorkflows(context.Context, []int64) (map[int64]*SpaceWorkflow, error)
}

type SpaceWorkflowAdmin interface {
	SaveSpaceWorkflow(context.Context, int64, SpaceWorkflow) (*SpaceWorkflowReceipt, error)
}

var spaceSourceIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var ErrSpaceWorkflowConflict = infraerrors.Conflict("SPACE_WORKFLOW_CONFLICT", "Space 标记来源、版本或账号身份不匹配")

func (v *SpaceWorkflow) Validate() error {
	v.Email = strings.ToLower(strings.TrimSpace(v.Email))
	address, err := mail.ParseAddress(v.Email)
	if err != nil || address.Address != v.Email || len(v.Email) > 254 || !spaceSourceIDPattern.MatchString(v.SourceID) || v.Revision < 1 || len(v.FlowID) == 0 || len(v.FlowID) > 200 || len(v.OrderID) > 200 || v.ChangedAt.IsZero() || v.ChangedAt.After(time.Now().Add(5*time.Minute)) {
		return infraerrors.BadRequest("INVALID_SPACE_WORKFLOW", "Space 标记参数无效")
	}
	switch v.Stage {
	case "merge", "merge_v2", "deactivate", "merchant":
	default:
		return infraerrors.BadRequest("INVALID_SPACE_STAGE", "无效的 Space 最后一步")
	}
	return nil
}

func (s *adminServiceImpl) SaveSpaceWorkflow(ctx context.Context, id int64, v SpaceWorkflow) (*SpaceWorkflowReceipt, error) {
	if err := v.Validate(); err != nil {
		return nil, err
	}
	account, err := s.accountRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if !account.IsOpenAISIWC() || !strings.EqualFold(strings.TrimSpace(account.GetCredential("email")), v.Email) {
		return nil, ErrSpaceWorkflowConflict
	}
	repo, ok := s.accountRepo.(SpaceWorkflowRepository)
	if !ok {
		return nil, errors.New("Space workflow repository unavailable")
	}
	return repo.SaveSpaceWorkflow(ctx, id, v)
}

func (s *adminServiceImpl) loadSpaceWorkflows(ctx context.Context, accounts []Account) error {
	ids := make([]int64, 0, len(accounts))
	for _, a := range accounts {
		if a.IsOpenAISIWC() {
			ids = append(ids, a.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	repo, ok := s.accountRepo.(SpaceWorkflowRepository)
	if !ok {
		return nil
	} // Existing test/optional repository implementations.
	rows, err := repo.SpaceWorkflows(ctx, ids)
	if err != nil {
		return err
	}
	for i := range accounts {
		accounts[i].SpaceWorkflow = rows[accounts[i].ID]
	}
	return nil
}
