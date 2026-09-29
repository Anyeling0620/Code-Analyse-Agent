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
	if err := db.AutoMigrate(&model.CostRecord{}); err != nil {
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
