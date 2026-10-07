package api

import (
	"strings"
	"testing"

	"edu.agent.code/service/do"
	"edu.agent.code/service/dto"
)

// PUT /api/profile 的请求体归一化：去空白、描述按字符截断、保留客户端看不到的 auth_subject。
func TestBuildProfileUpdateNormalizesAndTruncates(t *testing.T) {
	longDescription := strings.Repeat("描", descriptionMaxRunes+50)
	got, err := buildProfileUpdate("u1", &dto.ProfileUpdateReq{
		UserType:     "  student ",
		SkillLevel:   "\t入门\n",
		GoalType:     "做项目",
		Description:  "  " + longDescription + "  ",
		CurrentTopic: "Go Agent Eino 实战",
		CurrentStage: "开发中",
	}, &do.Profile{UserID: "u1", AuthSubject: "subject-keep"})
	if err != nil {
		t.Fatalf("归一化不该失败：%v", err)
	}
	if got.UserType != "student" {
		t.Fatalf("首尾空白未清理：%q", got.UserType)
	}
	if got.SkillLevel != "入门" {
		t.Fatalf("制表符/换行未清理：%q", got.SkillLevel)
	}
	if n := len([]rune(got.Description)); n != descriptionMaxRunes {
		t.Fatalf("描述应按字符截断到 %d，实际 %d", descriptionMaxRunes, n)
	}
	if got.AuthSubject != "subject-keep" {
		t.Fatalf("旧行的 auth_subject 不该被清空：%q", got.AuthSubject)
	}
	if got.UserID != "u1" {
		t.Fatalf("归属应当是登录用户：%q", got.UserID)
	}
}

// 归属只取入参里的登录 user_id，不理会旧行里的 user_id——否则一次请求就能写到别人名下。
func TestBuildProfileUpdateKeepsLoginUserID(t *testing.T) {
	got, err := buildProfileUpdate("me", &dto.ProfileUpdateReq{UserType: "student"},
		&do.Profile{UserID: "someone-else", AuthSubject: "s"})
	if err != nil {
		t.Fatalf("不该失败：%v", err)
	}
	if got.UserID != "me" {
		t.Fatalf("归属被旧行覆盖：%q", got.UserID)
	}
}

// PUT 是全量替换：全空请求必须被拒绝，否则一次误调用就把画像清空。
func TestBuildProfileUpdateRejectsAllEmpty(t *testing.T) {
	if _, err := buildProfileUpdate("u1", &dto.ProfileUpdateReq{Description: "   "}, nil); err == nil {
		t.Fatal("全空请求应当被拒绝")
	}
	// 只填描述也算有内容（描述是价值最高的字段）。
	got, err := buildProfileUpdate("u1", &dto.ProfileUpdateReq{Description: "只有描述"}, nil)
	if err != nil {
		t.Fatalf("只有描述时不该被拒绝：%v", err)
	}
	if got.Description != "只有描述" {
		t.Fatalf("描述未保留：%q", got.Description)
	}
}

// 库里没有记录时，GET 也要返回 user_id 已填的空画像（不是 nil），前端才能安全回填。
func TestProfileToDTOForMissingRecord(t *testing.T) {
	empty := profileToDTO("u1", nil)
	if empty == nil {
		t.Fatal("没有记录时不该返回 nil")
	}
	if empty.UserID != "u1" || empty.Description != "" {
		t.Fatalf("空画像不符合预期：%+v", empty)
	}

	filled := profileToDTO("u1", &do.Profile{
		UserID:      "u1",
		Description: "本科在读",
		UserType:    "student",
	})
	if filled.Description != "本科在读" || filled.UserType != "student" {
		t.Fatalf("字段未映射完整：%+v", filled)
	}
}
