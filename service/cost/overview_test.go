package cost

import (
	"math"
	"testing"
	"time"

	"edu.agent.code/service/do"
	"edu.agent.code/service/dto"
)

func near(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func findItem(items []dto.CostOverviewItem, key string) (dto.CostOverviewItem, bool) {
	for _, item := range items {
		if item.Key == key {
			return item, true
		}
	}
	return dto.CostOverviewItem{}, false
}

// 聚合口径：区间外的记录入口即被仓储层过滤，这里只验证 service 的汇总、占比与排序。
func TestBuildOverviewAggregates(t *testing.T) {
	from := time.Date(2026, 9, 21, 0, 0, 0, 0, time.Local)
	to := time.Date(2026, 10, 20, 0, 0, 0, 0, time.Local)
	rows := []do.CostOverviewRow{
		{
			OccurredAt:       time.Date(2026, 9, 21, 9, 0, 0, 0, time.Local),
			SessionID:        "sess_a",
			SessionTitle:     "重构分析",
			Model:            "deepseek-flash",
			ToolName:         "repo_analyzer",
			PromptTokens:     100,
			CachedTokens:     60,
			CompletionTokens: 20,
			EstimatedCNY:     0.5,
			CacheHitCNY:      0.06,
			CacheMissCNY:     0.44,
		},
		{
			// 本地凌晨：证明按 time.Local 归日，而不是按 UTC 或字面日期。
			OccurredAt:       time.Date(2026, 10, 20, 0, 30, 0, 0, time.Local),
			SessionID:        "sess_b",
			SessionTitle:     "",
			Model:            "gpt-4o",
			ToolName:         "", // 直调，归入 (direct)
			PromptTokens:     10,
			CachedTokens:     0,
			CompletionTokens: 5,
			EstimatedCNY:     0.25,
			CacheHitCNY:      0,
			CacheMissCNY:     0.25,
		},
	}

	got := buildOverview(from, to, rows)

	if got.From != "2026-09-21" || got.To != "2026-10-20" {
		t.Fatalf("返回区间不对：%s ~ %s", got.From, got.To)
	}
	total := got.Total
	if total.Calls != 2 || total.Sessions != 2 || total.Tools != 2 || total.Models != 2 {
		t.Fatalf("计数不对：%+v", total)
	}
	if !near(total.CNY, 0.75) || total.Tokens != 135 || total.PromptTokens != 110 || total.CompletionTokens != 25 {
		t.Fatalf("总量不对：%+v", total)
	}
	// 未命中 token 用 prompt-cached 推导，与 DailyUsage 同口径。
	if total.CacheHitTokens != 60 || total.CacheMissTokens != 50 {
		t.Fatalf("缓存 token 拆分不对：%+v", total)
	}
	if !near(total.CacheHitCNY, 0.06) || !near(total.CacheMissCNY, 0.69) {
		t.Fatalf("缓存成本拆分不对：%+v", total)
	}

	// 按成本降序，占比保留 4 位小数。
	if len(got.ByTool) != 2 || got.ByTool[0].Key != "repo_analyzer" || !near(got.ByTool[0].Share, 0.6667) {
		t.Fatalf("by_tool 排序/占比不对：%+v", got.ByTool)
	}
	direct, ok := findItem(got.ByTool, "(direct)")
	if !ok || !near(direct.Share, 0.3333) || direct.Calls != 1 {
		t.Fatalf("空 tool_name 应归入 (direct)：%+v", got.ByTool)
	}
	if len(got.ByModel) != 2 || got.ByModel[0].Key != "deepseek-flash" || !near(got.ByModel[0].Share, 0.6667) {
		t.Fatalf("by_model 不对：%+v", got.ByModel)
	}
	if len(got.BySession) != 2 {
		t.Fatalf("by_session 应有 2 项：%+v", got.BySession)
	}
	sessA, _ := findItem(got.BySession, "sess_a")
	if sessA.Label != "重构分析" || sessA.LastAt != "2026-09-21T09:00:00+08:00" {
		t.Fatalf("会话标题 / 最近调用时间不对：%+v", sessA)
	}
	sessB, _ := findItem(got.BySession, "sess_b")
	if sessB.Label != "" {
		t.Fatalf("无标题会话应为空串，实际 %q", sessB.Label)
	}

	// daily 覆盖区间每一天（09-21 ~ 10-20 共 30 天），无数据的补 0。
	if len(got.Daily) != 30 {
		t.Fatalf("daily 应补齐 30 天，实际 %d", len(got.Daily))
	}
	if got.Daily[0].Date != "2026-09-21" || got.Daily[29].Date != "2026-10-20" {
		t.Fatalf("daily 首尾日期不对：%s / %s", got.Daily[0].Date, got.Daily[29].Date)
	}
	if got.Daily[0].Calls != 1 || !near(got.Daily[0].CNY, 0.5) || got.Daily[0].Tokens != 120 {
		t.Fatalf("首日数据不对：%+v", got.Daily[0])
	}
	if got.Daily[29].Calls != 1 || !near(got.Daily[29].CNY, 0.25) {
		t.Fatalf("末日数据不对：%+v", got.Daily[29])
	}
	for i := 1; i < 29; i++ {
		if got.Daily[i].Calls != 0 || got.Daily[i].Tokens != 0 || got.Daily[i].CNY != 0 {
			t.Fatalf("无记录的日子应补 0，第 %d 天为 %+v", i, got.Daily[i])
		}
	}
}

// 区间内没有任何记录时：总量全 0、占比全 0、daily 仍然逐日补齐，
// 三个列表都要是非 nil 的空切片（JSON 里应是 []，不是 null）。
func TestBuildOverviewEmptyRange(t *testing.T) {
	from := time.Date(2026, 10, 19, 0, 0, 0, 0, time.Local)
	to := time.Date(2026, 10, 20, 0, 0, 0, 0, time.Local)

	got := buildOverview(from, to, nil)

	if got.Total.CNY != 0 || got.Total.Calls != 0 || got.Total.Sessions != 0 || got.Total.Tools != 0 || got.Total.Models != 0 {
		t.Fatalf("空区间总量应全 0：%+v", got.Total)
	}
	if got.ByTool == nil || got.ByModel == nil || got.BySession == nil {
		t.Fatalf("空列表应是空切片而不是 nil：%+v", got)
	}
	if len(got.ByTool) != 0 || len(got.ByModel) != 0 || len(got.BySession) != 0 {
		t.Fatalf("空区间不应有分项：%+v", got)
	}
	if len(got.Daily) != 2 || got.Daily[0].Date != "2026-10-19" || got.Daily[1].Date != "2026-10-20" {
		t.Fatalf("daily 应补齐两天：%+v", got.Daily)
	}
	for _, point := range got.Daily {
		if point.Calls != 0 || point.Tokens != 0 || point.CNY != 0 {
			t.Fatalf("空区间每日点应全 0：%+v", point)
		}
	}
}

// 总成本为 0（有调用但单价缺失、记为 0 成本）时 share 必须为 0，不能除零出 NaN。
func TestBuildOverviewZeroCostShare(t *testing.T) {
	from := time.Date(2026, 10, 20, 0, 0, 0, 0, time.Local)
	to := from
	rows := []do.CostOverviewRow{{
		OccurredAt:       from.Add(2 * time.Hour),
		SessionID:        "sess_a",
		Model:            "unknown",
		ToolName:         "chat",
		PromptTokens:     10,
		CompletionTokens: 5,
	}}

	got := buildOverview(from, to, rows)
	if got.Total.Calls != 1 || got.Total.CNY != 0 {
		t.Fatalf("总量不对：%+v", got.Total)
	}
	if len(got.ByTool) != 1 || got.ByTool[0].Share != 0 {
		t.Fatalf("总成本为 0 时 share 应为 0：%+v", got.ByTool)
	}
}
