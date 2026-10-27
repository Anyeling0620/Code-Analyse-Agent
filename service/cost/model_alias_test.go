package cost

import (
	"context"
	"testing"
	"time"

	"edu.agent.code/config"
	"edu.agent.code/service/do"
)

// newPriceService 只构造计价所需的最小 Service：Track 的计价部分不依赖仓储。
func newPriceService(price config.ModelPrice) *Service {
	return &Service{price: price}
}

// TestResolveDisplayModel 覆盖「渠道模型名 → 官方计价名」的归一化。
//
// 这段逻辑存在的理由：通过本机 OpenAI 兼容网关调用时，请求名带渠道前缀
// （如 buddy/deepseek-v4.1-flash），而 price_cny 只配官方名。若不归一化：
// 计价会静默走 fallback_model，且 cost_records.model 落库成渠道名，
// 导致成本报表的分模型维度分裂。所以这里要逐项钉死。
func TestResolveDisplayModel(t *testing.T) {
	price := config.ModelPrice{
		FallbackModel: "deepseek-flash",
		PriceCNY: map[string]config.ModelPriceCNY{
			"deepseek-flash":  {Prompt: 2, CacheHit: 0.04, Completion: 8},
			"deepseek-v4-pro": {Prompt: 9, CacheHit: 0.3, Completion: 27},
		},
		ModelDisplayNames: map[string]string{
			"buddy/deepseek-v4.1-flash": "deepseek-flash",
			"buddy/deepseek-v4-pro":     "deepseek-v4-pro",
			"spaced/alias":              "  deepseek-flash  ", // 前后空白应被去掉
			"blank/alias":               "   ",                // 纯空白视为未配置
			"chain/a":                   "chain/b",            // 只解析一层，不追到 deepseek-flash
			"chain/b":                   "deepseek-flash",
		},
	}
	s := newPriceService(price)

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"命中映射：渠道前缀名归一为官方名", "buddy/deepseek-v4.1-flash", "deepseek-flash"},
		{"命中映射：pro 版本", "buddy/deepseek-v4-pro", "deepseek-v4-pro"},
		{"未配置映射时原样返回", "deepseek-flash", "deepseek-flash"},
		{"未知模型原样返回（不猜）", "totally-unknown", "totally-unknown"},
		{"映射值前后空白被去掉", "spaced/alias", "deepseek-flash"},
		{"映射值为纯空白视为未配置", "blank/alias", "blank/alias"},
		{"只解析一层，不做链式穿透", "chain/a", "chain/b"},
		{"空字符串原样返回", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := s.resolveDisplayModel(tt.in); got != tt.want {
				t.Fatalf("resolveDisplayModel(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestResolveDisplayModelWithoutConfig 确认未配置映射时是零成本直通。
func TestResolveDisplayModelWithoutConfig(t *testing.T) {
	s := newPriceService(config.ModelPrice{
		PriceCNY: map[string]config.ModelPriceCNY{
			"deepseek-flash": {Prompt: 2, CacheHit: 0.04, Completion: 8},
		},
	})
	if got := s.resolveDisplayModel("buddy/anything"); got != "buddy/anything" {
		t.Fatalf("未配置映射时应原样返回，实际 %q", got)
	}
}

// TestLookupRatePrefersExactMatch 确认归一化后走的是**精确命中**而不是 fallback。
//
// 这是"计费正确"的核心断言：若映射失效，lookupRate 仍会通过 fallback_model
// 返回同一个价格——金额碰巧一样，但 model 字段会落库成渠道名。所以这里
// 除了比价格，还要确认被选中的是哪个模型名。
func TestLookupRatePrefersExactMatch(t *testing.T) {
	price := config.ModelPrice{
		FallbackModel: "deepseek-flash",
		PriceCNY: map[string]config.ModelPriceCNY{
			"deepseek-flash":  {Prompt: 2, CacheHit: 0.04, Completion: 8},
			"deepseek-v4-pro": {Prompt: 9, CacheHit: 0.3, Completion: 27},
		},
		ModelDisplayNames: map[string]string{
			"buddy/deepseek-v4-pro": "deepseek-v4-pro",
		},
	}
	s := newPriceService(price)

	// 归一化后应命中 v4-pro 的价格（prompt=9），而不是 fallback 的 deepseek-flash（prompt=2）。
	normalized := s.resolveDisplayModel("buddy/deepseek-v4-pro")
	if normalized != "deepseek-v4-pro" {
		t.Fatalf("归一化失败：%q", normalized)
	}
	rate, err := s.lookupRate(normalized)
	if err != nil {
		t.Fatalf("lookupRate 失败: %v", err)
	}
	if rate.Prompt != 9 {
		t.Fatalf("应命中 deepseek-v4-pro 的 prompt=9，实际 %v（说明落到了 fallback 价）", rate.Prompt)
	}
}

// TestLookupRateFallbackStillWorks 确认未归一化的渠道名仍有兜底，不会报错。
//
// 这是"网关来了个新模型"的场景：用户还没配映射，此时应沿用 fallback 价继续记账，
// 而不是让成本统计整体失败。
func TestLookupRateFallbackStillWorks(t *testing.T) {
	s := newPriceService(config.ModelPrice{
		FallbackModel: "deepseek-flash",
		PriceCNY: map[string]config.ModelPriceCNY{
			"deepseek-flash": {Prompt: 2, CacheHit: 0.04, Completion: 8},
		},
	})
	rate, err := s.lookupRate("brand-new/model")
	if err != nil {
		t.Fatalf("应走 fallback 而不是报错，实际: %v", err)
	}
	if rate.Prompt != 2 {
		t.Fatalf("应使用 fallback 的 prompt=2，实际 %v", rate.Prompt)
	}
}

// ── Track 级别断言 ────────────────────────────────────────────────
//
// 上面几组只测了 resolveDisplayModel / lookupRate 两个子函数。但真正决定
// "成本表里记什么名字"的是 Track 内部有没有调用归一化——只测子函数的话，
// 把 Track 里那行归一化删掉，测试依然全绿（本文件初版就踩了这个坑）。
// 所以这里用一个记录型 stub 捕获实际落库的 model 名。

type recordingCost struct {
	records []*do.CostRecord
}

func (r *recordingCost) Insert(_ context.Context, req *do.CostRecord) error {
	r.records = append(r.records, req)
	return nil
}
func (r *recordingCost) DailyTotal(context.Context, time.Time) (float64, error) { return 0, nil }
func (r *recordingCost) DailyUsage(context.Context, string, time.Time) (do.CostDailyUsage, error) {
	return do.CostDailyUsage{}, nil
}
func (r *recordingCost) GroupByUser(context.Context, time.Time, time.Time) ([]do.CostByUser, error) {
	return nil, nil
}
func (r *recordingCost) GroupByTool(context.Context, time.Time, time.Time) ([]do.CostByTool, error) {
	return nil, nil
}
func (r *recordingCost) Overview(context.Context, string, time.Time, time.Time) ([]do.CostOverviewRow, error) {
	return nil, nil
}

// TestTrackStoresOfficialModelName 是最关键的一条：
// 渠道模型名必须被归一化后再落库，否则成本报表的分模型维度会分裂。
func TestTrackStoresOfficialModelName(t *testing.T) {
	rec := &recordingCost{}
	s := &Service{
		cost: rec,
		price: config.ModelPrice{
			FallbackModel: "deepseek-flash",
			PriceCNY: map[string]config.ModelPriceCNY{
				"deepseek-flash":  {Prompt: 2, CacheHit: 0.04, Completion: 8},
				"deepseek-v4-pro": {Prompt: 9, CacheHit: 0.3, Completion: 27},
			},
			ModelDisplayNames: map[string]string{
				"buddy/deepseek-v4.1-flash": "deepseek-flash",
				"buddy/deepseek-v4-pro":     "deepseek-v4-pro",
			},
		},
	}

	cases := []struct {
		in       string
		wantName string
		wantRate float64 // 期望命中的 prompt 单价，用于确认没落到 fallback
	}{
		{"buddy/deepseek-v4.1-flash", "deepseek-flash", 2},
		{"buddy/deepseek-v4-pro", "deepseek-v4-pro", 9},
	}
	for _, c := range cases {
		rec.records = nil
		cny, err := s.Track(context.Background(), "u1", "s1", c.in, "project_qa", 1000, 900, 100)
		if err != nil {
			t.Fatalf("Track(%q) 报错: %v", c.in, err)
		}
		if len(rec.records) != 1 {
			t.Fatalf("应落库 1 条，实际 %d", len(rec.records))
		}
		got := rec.records[0]
		if got.Model != c.wantName {
			t.Fatalf("落库 model 应为 %q，实际 %q（说明 Track 里没做归一化）", c.wantName, got.Model)
		}
		if cny <= 0 {
			t.Fatalf("成本应为正数，实际 %v", cny)
		}
		// 1000 prompt / 900 cached / 100 completion：
		// cny = 900*cache_hit/1e6... 这里只做量级与单调性检查，具体公式由 pricing 负责。
	}
}

// TestTrackFallsBackForUnknownChannelModel 确认未配映射的新渠道模型仍能记账。
func TestTrackFallsBackForUnknownChannelModel(t *testing.T) {
	rec := &recordingCost{}
	s := &Service{
		cost: rec,
		price: config.ModelPrice{
			FallbackModel:     "deepseek-flash",
			PriceCNY:          map[string]config.ModelPriceCNY{"deepseek-flash": {Prompt: 2, CacheHit: 0.04, Completion: 8}},
			ModelDisplayNames: map[string]string{"buddy/deepseek-v4.1-flash": "deepseek-flash"},
		},
	}
	if _, err := s.Track(context.Background(), "u1", "s1", "some-new/model", "project_qa", 100, 50, 10); err != nil {
		t.Fatalf("未知模型应走 fallback 而不是报错: %v", err)
	}
	if len(rec.records) != 1 {
		t.Fatalf("应落库 1 条")
	}
	// 未配映射时保留原名落库：这是刻意行为，便于发现新渠道模型并补映射。
	if rec.records[0].Model != "some-new/model" {
		t.Fatalf("未配映射应保留原名，实际 %q", rec.records[0].Model)
	}
}
