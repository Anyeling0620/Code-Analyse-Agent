package adaptor

import (
	"path/filepath"
	"testing"

	"edu.agent.code/adaptor/repo/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// 老库升级时，profiles 里已有当前值但没有历史。首次启动应补一条 legacy 快照，
// 且重复执行不会重复插入（幂等）。
func TestBackfillProfileRevisionsIsIdempotent(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "revisions.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试库失败：%v", err)
	}
	closeDBOnCleanup(t, db)
	if err := db.AutoMigrate(&model.Profile{}, &model.ProfileRevision{}); err != nil {
		t.Fatalf("建表失败：%v", err)
	}
	if err := db.Exec(`INSERT INTO profiles (user_id, user_type, skill_level, goal_type, description, current_topic, current_stage)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"legacy_user", "student", "入门", "做项目", "历史描述", "Eino", "学习中").Error; err != nil {
		t.Fatalf("写入历史画像失败：%v", err)
	}

	if err := backfillProfileRevisions(db); err != nil {
		t.Fatalf("首次回填失败：%v", err)
	}
	if err := backfillProfileRevisions(db); err != nil {
		t.Fatalf("重复回填失败：%v", err)
	}

	var count int64
	if err := db.Model(&model.ProfileRevision{}).
		Where("user_id = ?", "legacy_user").Count(&count).Error; err != nil {
		t.Fatalf("统计快照失败：%v", err)
	}
	if count != 1 {
		t.Fatalf("同一用户只应有一条 legacy 快照，实际 %d 条", count)
	}
	var revision model.ProfileRevision
	if err := db.Where("user_id = ?", "legacy_user").First(&revision).Error; err != nil {
		t.Fatalf("读取快照失败：%v", err)
	}
	if revision.Source != "legacy" || revision.Description != "历史描述" || revision.CurrentTopic != "Eino" {
		t.Fatalf("回填内容不符：%+v", revision)
	}
}

// 已有快照的用户不参与回填：否则会把当前值伪装成一条新变更。
func TestBackfillProfileRevisionsSkipsUsersWithHistory(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "revisions_existing.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试库失败：%v", err)
	}
	closeDBOnCleanup(t, db)
	if err := db.AutoMigrate(&model.Profile{}, &model.ProfileRevision{}); err != nil {
		t.Fatalf("建表失败：%v", err)
	}
	if err := db.Exec(`INSERT INTO profiles (user_id, current_topic) VALUES (?, ?)`, "u_has", "当前值").Error; err != nil {
		t.Fatalf("写入画像失败：%v", err)
	}
	if err := db.Exec(`INSERT INTO profile_revisions (user_id, current_topic, source) VALUES (?, ?, ?)`,
		"u_has", "更早的值", "api").Error; err != nil {
		t.Fatalf("写入已有快照失败：%v", err)
	}

	if err := backfillProfileRevisions(db); err != nil {
		t.Fatalf("回填失败：%v", err)
	}
	var count int64
	if err := db.Model(&model.ProfileRevision{}).
		Where("user_id = ?", "u_has").Count(&count).Error; err != nil {
		t.Fatalf("统计快照失败：%v", err)
	}
	if count != 1 {
		t.Fatalf("已有快照的用户不该被回填，实际 %d 条", count)
	}
}
