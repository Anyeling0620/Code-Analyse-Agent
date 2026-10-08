package self_report

import (
	"context"
	"edu.agent.code/common"
	"testing"
)

// TestAllowed 锁定白名单门禁语义。
//
// 这是自省数据唯一的访问控制边界（工具集是全局构建的，无法按用户挂载），
// 所以"漏配白名单"必须等于"谁都不放行"，而不是"全都放行"。
func TestAllowed(t *testing.T) {
	cases := []struct {
		name        string
		allowedUser []string
		ctxUserID   string
		want        bool
	}{
		{"空白名单谁都不放行", nil, "Anyeling", false},
		{"空白名单即使 ctx 有用户也不放行", []string{}, "Anyeling", false},
		{"白名单外的用户拒绝", []string{"Anyeling"}, "visitor", false},
		{"白名单内的用户放行", []string{"Anyeling"}, "Anyeling", true},
		{"ctx 里没有用户（空串）拒绝", []string{"Anyeling"}, "", false},
		{"空串项不构成白名单条目", []string{"", ""}, "Anyeling", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.ctxUserID != "" {
				ctx = common.WithUserAndSession(ctx, tc.ctxUserID, "s1")
			}
			if got := allowed(ctx, buildAllowSet(tc.allowedUser)); got != tc.want {
				t.Fatalf("allowed(allowed=%v, ctxUser=%q) = %v, want %v",
					tc.allowedUser, tc.ctxUserID, got, tc.want)
			}
		})
	}
}
