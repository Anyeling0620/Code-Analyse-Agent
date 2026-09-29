package auth

import (
	"edu.agent.code/common"
	"edu.agent.code/config"
	"strings"
	"testing"
	"time"
)

// 同一个 IP + 同一浏览器必须派生同一个身份，否则配额会在每次登录后被重置。
func TestGuestIdentityIsStable(t *testing.T) {
	first := GuestIdentity("1.2.3.4", "abc123")
	if first != GuestIdentity("1.2.3.4", "abc123") {
		t.Fatalf("相同 IP 与指纹应得到相同身份，得到 %s 与 %s", first, GuestIdentity("1.2.3.4", "abc123"))
	}
	if !strings.HasPrefix(first, common.GuestUserPrefix) {
		t.Fatalf("游客身份应带 %s 前缀，得到 %s", common.GuestUserPrefix, first)
	}
	if !common.IsGuestUser(first) {
		t.Fatalf("IsGuestUser 应识别游客身份 %s", first)
	}
	if common.IsGuestUser("visitor") {
		t.Fatal("普通账号不应被识别为游客")
	}
}

// 换 IP 或换浏览器都应换身份，避免多人共用一份游客额度。
func TestGuestIdentityIsScoped(t *testing.T) {
	base := GuestIdentity("1.2.3.4", "abc123")
	if base == GuestIdentity("1.2.3.5", "abc123") {
		t.Fatal("不同 IP 应得到不同身份")
	}
	if base == GuestIdentity("1.2.3.4", "abc124") {
		t.Fatal("不同浏览器指纹应得到不同身份")
	}
}

// 指纹缺失时退化为只按 IP 识别，仍然可用，不能因为缺头就登不进去。
func TestGuestIdentityWithoutFingerprint(t *testing.T) {
	got := GuestIdentity("1.2.3.4", "  ")
	if got != GuestIdentity("1.2.3.4", "") {
		t.Fatalf("指纹为空串与全空格应等价，得到 %s 与 %s", got, GuestIdentity("1.2.3.4", ""))
	}
	if got == GuestIdentity("1.2.3.5", "") {
		t.Fatal("无指纹时不同 IP 仍应得到不同身份")
	}
}

// 游客默认 plus 计划；套餐留空或填错都回落到 plus，而不是无配额。
func TestBuildGuestPlan(t *testing.T) {
	cases := map[string]common.Plan{
		"":        common.PlanPlus,
		"  ":      common.PlanPlus,
		"pro":     common.PlanPro,
		"unknown": common.PlanPlus,
	}
	for configured, want := range cases {
		got := buildGuest(config.Auth{Guest: config.Guest{Plan: configured}}).plan
		if got != want {
			t.Errorf("plan=%q 应得到 %s，实际 %s", configured, want, got)
		}
	}
}

// 游客令牌默认 24 小时，且不超过账号令牌的有效期（配置错误时不能让游客令牌活得比账号还久）。
func TestGuestTokenTTL(t *testing.T) {
	if got := guestTokenTTL(config.Auth{}); got != 24*time.Hour {
		t.Errorf("默认游客令牌有效期应为 24h，实际 %v", got)
	}
	if got := guestTokenTTL(config.Auth{TokenTTLHours: 2, Guest: config.Guest{TokenTTLHours: 72}}); got != 2*time.Hour {
		t.Errorf("游客令牌不应长于账号令牌，实际 %v", got)
	}
}
