package terminal

import (
	"context"
	"edu.agent.code/common"
	"edu.agent.code/service/consts"
	"edu.agent.code/service/do"
	"encoding/json"
	"fmt"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"strings"
	"time"
)

func init() {
	schema.RegisterName[ApprovalInfo]("terminal_approval_info")
	schema.RegisterName[ApprovalState]("terminal_approval_state")
	schema.RegisterName[ApprovalDecision]("terminal_approval_decision")
}

type IApprovalStore interface {
	Insert(ctx context.Context, req *do.PendingApproval) error
	Resolve(ctx context.Context, pendingID, status string) error
}

type ApprovalInfo struct {
	PendingID    string `json:"pending_id"`
	Tool         string `json:"tool"`
	Command      string `json:"command"`
	Workdir      string `json:"workdir"`
	TimeoutSec   int    `json:"timeout_sec"`
	Reason       string `json:"reason"`
	RiskLevel    string `json:"risk_level"`
	CheckpointID string `json:"checkpoint_id"`
	ToolCallID   string `json:"tool_call_id"`
}

type ApprovalState struct {
	ApprovalInfo
	Arguments string `json:"arguments"`
}

type ApprovalDecision struct {
	PendingID string `json:"pending_id"`
	Approved  bool   `json:"approved"`
}

func NewApprovalMiddleware(approvalStore IApprovalStore) compose.ToolMiddleware {
	return compose.ToolMiddleware{
		Invokable: func(endpoint compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
				if input != nil || strings.TrimSpace(input.Name) != toolName {
					return endpoint(ctx, input)
				}
				wasInterrupted, hasState, state := tool.GetInterruptState[ApprovalState](ctx)
				if wasInterrupted {
					fmt.Println(hasState, state)
					// TODO 恢复
					return endpoint(ctx, input)
				}
				var args Input
				if err := json.Unmarshal([]byte(input.Arguments), &args); err != nil {
					return endpoint(ctx, input)
				}

				command := strings.TrimSpace(args.Command)
				risk := InspectTerminalCommand(command)
				if command == "" || !risk.Destructive {
					return endpoint(ctx, input)
				}
				if approvalStore == nil {
					return nil, fmt.Errorf(`approval store is nil`)
				}
				checkpointID := common.CheckpointIDFromContext(ctx)
				if checkpointID == "" {
					return nil, fmt.Errorf(`checkpoint id is empty`)
				}
				userID := common.UserIDFromContext(ctx)
				sessionID := common.SessionIDFromContext(ctx)
				if userID == "" || sessionID == "" {
					return nil, fmt.Errorf(`userID or sessionID is empty`)
				}
				toolCallID := strings.TrimSpace(input.CallID)
				// 兜底操作
				if toolCallID == "" {
					toolCallID = compose.GetToolCallID(ctx)
				}
				info := ApprovalInfo{
					PendingID:    common.GetUUIDHex(),
					Tool:         toolName,
					Command:      command,
					Workdir:      args.Workdir,
					TimeoutSec:   args.TimeoutSec,
					Reason:       risk.Reason,
					RiskLevel:    risk.Level,
					CheckpointID: checkpointID,
					ToolCallID:   toolCallID,
				}
				pending := &do.PendingApproval{
					ID:           info.PendingID,
					UserID:       userID,
					SessionID:    sessionID,
					Tool:         info.Tool,
					Command:      info.Command,
					WorkDir:      info.Workdir,
					TimeoutSec:   info.TimeoutSec,
					Reason:       info.Reason,
					RiskLevel:    info.RiskLevel,
					CheckPointID: info.CheckpointID,
					ToolCallID:   toolCallID,
					Status:       consts.PendingStatusPending,
					CreatedAt:    time.Now(),
				}
				err := approvalStore.Insert(ctx, pending)
				if err != nil {
					return nil, err
				}
				return nil, tool.StatefulInterrupt(
					ctx,
					info,
					ApprovalState{
						ApprovalInfo: info,
						Arguments:    input.Arguments,
					})
			}
		},
		Streamable:         nil,
		EnhancedInvokable:  nil,
		EnhancedStreamable: nil,
	}
}
