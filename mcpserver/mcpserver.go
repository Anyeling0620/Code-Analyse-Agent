package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"edu.agent.code/common"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type QuotaService interface {
	Today(ctx context.Context, userID, day string) (int64, error)
}

type CostService interface {
	DailyTotal(ctx context.Context, day time.Time) (float64, error)
}

type Server struct {
	quotaSvc QuotaService
	costSvc  CostService
}

func NewServer(quotaSvc QuotaService, costSvc CostService) *Server {
	return &Server{
		quotaSvc: quotaSvc,
		costSvc:  costSvc,
	}
}

func (s *Server) Build() *server.MCPServer {
	svc := server.NewMCPServer(
		"edu-agent-code",
		"1.0.0",
		server.WithToolCapabilities(true),
	)
	s.registerTodayQuota(svc)
	s.registerDailyCost(svc)
	return svc
}

func (s *Server) ServeHTTP(addr string) *http.Server {
	srv := s.Build()
	sseServer := server.NewSSEServer(srv)
	return &http.Server{
		Addr:    addr,
		Handler: sseServer,
	}
}

func demoUser() *common.UserInfo {
	return &common.UserInfo{
		UserID: "demo-user",
		Plan:   common.PlanPro,
	}
}
func (s *Server) registerTodayQuota(svc *server.MCPServer) {
	tool := mcp.NewTool("get_today_quota",
		mcp.WithDescription("查询 edu.agent.code 当前用户今日配额、已用次数和剩余额度。"+
			"适用：用户询问今天还能调用多少次、额度是否用完、当前套餐每日限制。"+
			"不适用：用户询问人民币成本或 token 成本时，请使用 get_daily_cost。"),
	)
	svc.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user := demoUser()
		today := time.Now().Format(time.DateOnly)
		used, err := s.quotaSvc.Today(ctx, user.UserID, today)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("查询配额失败：%v", err)), nil
		}
		resp := map[string]any{
			"user_id":   user.UserID,
			"plan":      user.Plan,
			"date":      today,
			"used":      used,
			"limit":     user.Plan.DailyQuota(),
			"remaining": max(int64(0), int64(user.Plan.DailyQuota())-used),
		}
		raw, _ := json.MarshalIndent(resp, "", "  ")
		return mcp.NewToolResultText(string(raw)), nil
	})
}

func (s *Server) registerDailyCost(svc *server.MCPServer) {
	tool := mcp.NewTool("get_daily_cost",
		mcp.WithDescription("查询 edu.agent.code 指定日期的模型调用成本，默认查询今天。"+
			"适用：用户询问今日用量、今日花费、今日成本、某天模型调用成本。"+
			"不适用：用户询问还剩多少请求次数时，请使用 get_today_quota。"),
		mcp.WithString("date",
			mcp.Description("日期，格式：YYYY-MM-DD，不传则查询服务器当前日期"),
		),
	)
	svc.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		day, _ := request.GetArguments()["date"].(string)
		day = strings.TrimSpace(day)
		var (
			err     error
			dayTime time.Time
		)
		if day != "" {
			dayTime, err = time.ParseInLocation(time.DateOnly, day, time.Local)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("日期格式错误：%v, date必须是YYYY-MM-DD格式", err)), nil
			}
		} else {
			dayTime = time.Now()
		}
		costCny, err := s.costSvc.DailyTotal(ctx, dayTime)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("查询成本小号失败：%v", err)), nil
		}
		resp := map[string]any{
			"date":     dayTime.Format(time.DateOnly),
			"cost_cny": costCny,
		}
		raw, _ := json.MarshalIndent(resp, "", "  ")
		return mcp.NewToolResultText(string(raw)), nil
	})
}
