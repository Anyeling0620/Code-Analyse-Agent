package cost

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"edu.agent.code/adaptor/repo/model"
	"edu.agent.code/service/do"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newTestCost(t *testing.T) *Cost {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "cost.db")), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// Overview 会 LEFT JOIN sessions 取会话标题，测试库也要有这张表。
	if err := db.AutoMigrate(&model.CostRecord{}, &model.Session{}); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	// Windows 下连接池不关闭会让 t.TempDir 清理失败（文件仍被占用），需显式 Close。
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil && sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	return &Cost{db: db}
}

func insertCost(t *testing.T, repo *Cost, userID string, prompt, cached, completion int64, cny float64, at time.Time) {
	t.Helper()
	if err := repo.Insert(context.Background(), &do.CostRecord{
		UserID:           userID,
		SessionID:        "s-" + userID,
		Model:            "deepseek-flash",
		PromptTokens:     prompt,
		CachedTokens:     cached,
		CacheMissTokens:  prompt - cached,
		CompletionTokens: completion,
		EstimatedCNY:     cny,
		CacheHitCNY:      cny / 2,
		CacheMissCNY:     cny / 2,
		OccurredAt:       at,
	}); err != nil {
		t.Fatalf("insert cost for %s: %v", userID, err)
	}
}

// 成本汇总必须按用户隔离：两个账号同一天各有记账，各自只能看到自己那一份。
func TestDailyUsageFiltersByUser(t *testing.T) {
	repo := newTestCost(t)
	ctx := context.Background()
	day := time.Date(2026, 9, 28, 10, 30, 0, 0, time.Local)

	insertCost(t, repo, "Anyeling", 100, 10, 20, 1.0, day)
	insertCost(t, repo, "visitor", 7, 0, 3, 0.25, day)

	any, err := repo.DailyUsage(ctx, "Anyeling", day)
	if err != nil {
		t.Fatalf("DailyUsage(Anyeling): %v", err)
	}
	if any.PromptTokens != 100 || any.CompletionTokens != 20 || any.CachedTokens != 10 {
		t.Fatalf("Anyeling 的 token 汇总不对：%+v", any)
	}
	if any.CacheMissTokens != 90 {
		t.Fatalf("未命中 token 应为 prompt-cached=90，实际 %d", any.CacheMissTokens)
	}
	if any.TotalCNY != 1.0 {
		t.Fatalf("Anyeling 的成本应为 1.0，实际 %v", any.TotalCNY)
	}

	vis, err := repo.DailyUsage(ctx, "visitor", day)
	if err != nil {
		t.Fatalf("DailyUsage(visitor): %v", err)
	}
	if vis.PromptTokens != 7 || vis.CompletionTokens != 3 || vis.CachedTokens != 0 || vis.TotalCNY != 0.25 {
		t.Fatalf("visitor 只能看到自己的用量，实际 %+v", vis)
	}
}

// 没有记账的用户应返回全 0，而不是串到别人的数据上。
func TestDailyUsageEmptyForUnknownUser(t *testing.T) {
	repo := newTestCost(t)
	day := time.Date(2026, 9, 28, 10, 30, 0, 0, time.Local)
	insertCost(t, repo, "Anyeling", 100, 10, 20, 1.0, day)

	got, err := repo.DailyUsage(context.Background(), "visitor", day)
	if err != nil {
		t.Fatalf("DailyUsage: %v", err)
	}
	if got.PromptTokens != 0 || got.CompletionTokens != 0 || got.TotalCNY != 0 {
		t.Fatalf("无记录用户应为全 0，实际 %+v", got)
	}
}

// 同一天之外的记录不应被计入。
func TestDailyUsageExcludesOtherDays(t *testing.T) {
	repo := newTestCost(t)
	day := time.Date(2026, 9, 28, 10, 30, 0, 0, time.Local)
	insertCost(t, repo, "Anyeling", 100, 10, 20, 1.0, day)
	insertCost(t, repo, "Anyeling", 999, 0, 999, 9.9, day.AddDate(0, 0, -1))

	got, err := repo.DailyUsage(context.Background(), "Anyeling", day)
	if err != nil {
		t.Fatalf("DailyUsage: %v", err)
	}
	if got.PromptTokens != 100 || got.TotalCNY != 1.0 {
		t.Fatalf("只应统计当天，实际 %+v", got)
	}
}

// insertCostDetail 用于构造带会话 / 工具 / 模型维度的记录，Overview 的用例需要这些列。
func insertCostDetail(t *testing.T, repo *Cost, userID, sessionID, toolName, modelName string,
	prompt, cached, completion int64, cny float64, at time.Time) {
	t.Helper()
	if err := repo.Insert(context.Background(), &do.CostRecord{
		UserID:           userID,
		SessionID:        sessionID,
		Model:            modelName,
		ToolName:         toolName,
		PromptTokens:     prompt,
		CachedTokens:     cached,
		CacheMissTokens:  prompt - cached,
		CompletionTokens: completion,
		EstimatedCNY:     cny,
		CacheHitCNY:      cny / 2,
		CacheMissCNY:     cny / 2,
		OccurredAt:       at,
	}); err != nil {
		t.Fatalf("insert cost detail for %s: %v", userID, err)
	}
}

// Overview 只取当前用户、区间 [from, to) 内的明细，并带出会话标题。
// 区间外的记录、别人的记录都必须排除，否则总览会变成全站数据。
func TestOverviewFiltersUserAndRange(t *testing.T) {
	repo := newTestCost(t)
	ctx := context.Background()
	from := time.Date(2026, 9, 21, 0, 0, 0, 0, time.Local)
	to := time.Date(2026, 10, 21, 0, 0, 0, 0, time.Local) // 开区间上界 = 10-20 的次日零点

	if err := repo.db.Create(&model.Session{SessionID: "sess_a", UserID: "Anyeling", Title: "重构分析"}).Error; err != nil {
		t.Fatalf("seed session: %v", err)
	}

	insertCostDetail(t, repo, "Anyeling", "sess_a", "repo_analyzer", "deepseek-flash", 100, 60, 20, 0.5,
		time.Date(2026, 9, 21, 9, 0, 0, 0, time.Local))
	// sess_b 没有对应会话行（可能已被删除），标题应回退为空串。
	insertCostDetail(t, repo, "Anyeling", "sess_b", "", "gpt-4o", 10, 0, 5, 0.25,
		time.Date(2026, 10, 20, 0, 30, 0, 0, time.Local))
	insertCostDetail(t, repo, "Anyeling", "sess_a", "repo_analyzer", "deepseek-flash", 999, 0, 999, 9.9,
		time.Date(2026, 9, 20, 23, 59, 0, 0, time.Local)) // 区间外
	insertCostDetail(t, repo, "visitor", "sess_v", "chat", "gpt-4o", 7, 0, 3, 5,
		time.Date(2026, 10, 1, 12, 0, 0, 0, time.Local)) // 他人

	rows, err := repo.Overview(ctx, "Anyeling", from, to)
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("应只有 2 条（当前用户 + 区间内），实际 %d 条：%+v", len(rows), rows)
	}

	bySession := map[string]do.CostOverviewRow{}
	for _, row := range rows {
		bySession[row.SessionID] = row
	}
	if got := bySession["sess_a"]; got.SessionTitle != "重构分析" {
		t.Fatalf("sess_a 应带出会话标题，实际 %q", got.SessionTitle)
	}
	if got := bySession["sess_b"]; got.SessionTitle != "" {
		t.Fatalf("会话行缺失时标题应为空串，实际 %q", got.SessionTitle)
	}
	if got := bySession["sess_b"]; got.PromptTokens != 10 || got.CompletionTokens != 5 || got.EstimatedCNY != 0.25 {
		t.Fatalf("sess_b 记录投影不对：%+v", got)
	}
}
