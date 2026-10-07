package adaptor

import (
	"path/filepath"
	"testing"

	"edu.agent.code/adaptor/repo/model"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// 旧的 purchased_courses（JSON 数组）要一次性回填到 description：
// 这是唯一会碰存量画像数据的代码路径，必须可重复执行、且不覆盖已有描述。
func TestMigrateProfileDescriptionBackfillsLegacyCourses(t *testing.T) {
	db := openLegacyProfileDB(t)

	// 旧数据：数组 → 顿号拼接。
	if err := db.Exec(`INSERT INTO profiles (user_id, purchased_courses, current_topic) VALUES (?, ?, ?)`,
		"u_courses", `["Go Agent Eino 实战","编译原理"]`, "topic").Error; err != nil {
		t.Fatalf("插入旧数据失败：%v", err)
	}
	// 空数组：没有可回填的内容，不该写入。
	if err := db.Exec(`INSERT INTO profiles (user_id, purchased_courses) VALUES (?, ?)`, "u_empty", `[]`).Error; err != nil {
		t.Fatalf("插入空数组失败：%v", err)
	}
	// 已有描述的行：不能被旧列覆盖。
	if err := db.Exec(`INSERT INTO profiles (user_id, purchased_courses, description) VALUES (?, ?, ?)`,
		"u_keep", `["旧课程"]`, "我自己写的描述").Error; err != nil {
		t.Fatalf("插入已有描述失败：%v", err)
	}

	if err := migrateProfileDescription(db); err != nil {
		t.Fatalf("回填失败：%v", err)
	}

	if got := descriptionOf(t, db, "u_courses"); got != "Go Agent Eino 实战、编译原理" {
		t.Fatalf("回填结果不符合预期：%q", got)
	}
	if got := descriptionOf(t, db, "u_empty"); got != "" {
		t.Fatalf("空数组不该产生描述，实际 %q", got)
	}
	if got := descriptionOf(t, db, "u_keep"); got != "我自己写的描述" {
		t.Fatalf("已有描述被覆盖：%q", got)
	}

	// 幂等：再跑一次，结果不变且不报错。
	if err := migrateProfileDescription(db); err != nil {
		t.Fatalf("重复执行应当无副作用：%v", err)
	}
	if got := descriptionOf(t, db, "u_courses"); got != "Go Agent Eino 实战、编译原理" {
		t.Fatalf("重复执行改动了数据：%q", got)
	}
}

// 全新库（没有旧列）不该报错，也不该做任何事。
func TestMigrateProfileDescriptionSkipsFreshDatabase(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "fresh.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试库失败：%v", err)
	}
	if err := db.AutoMigrate(&model.Profile{}); err != nil {
		t.Fatalf("建表失败：%v", err)
	}
	closeDBOnCleanup(t, db)
	if err := migrateProfileDescription(db); err != nil {
		t.Fatalf("全新库不该返回错误：%v", err)
	}
}

// 旧列里的脏数据（不是合法 JSON）按原文本回填，不能让整次迁移失败。
func TestJoinLegacyCoursesToleratesDirtyValue(t *testing.T) {
	cases := map[string]string{
		`["a","b"]`:         "a、b",
		`[" a ", "", "b "]`: "a、b",
		`不是 JSON`:           "不是 JSON",
		`[]`:                "",
		`[""]`:              "",
	}
	for raw, want := range cases {
		if got := joinLegacyCourses(raw); got != want {
			t.Fatalf("joinLegacyCourses(%q) = %q，期望 %q", raw, got, want)
		}
	}
}

// openLegacyProfileDB 造一个「旧版本」的 profiles 表（带 purchased_courses、没有 description），
// 再按生产路径跑一次 AutoMigrate 把新列加上去。
func openLegacyProfileDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "legacy.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试库失败：%v", err)
	}
	if err := db.Exec(`CREATE TABLE profiles (
		user_id text primary key,
		auth_subject text,
		user_type text,
		skill_level text,
		goal_type text,
		purchased_courses text,
		current_topic text,
		current_stage text,
		updated_at datetime
	)`).Error; err != nil {
		t.Fatalf("建旧表失败：%v", err)
	}
	if err := db.AutoMigrate(&model.Profile{}); err != nil {
		t.Fatalf("AutoMigrate 失败：%v", err)
	}
	closeDBOnCleanup(t, db)
	return db
}

// closeDBOnCleanup 在测试结束、TempDir 被删之前关掉连接。
// Windows 上文件句柄未释放会让 TempDir 清理直接失败。
func closeDBOnCleanup(t *testing.T, db *gorm.DB) {
	t.Helper()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("取底层连接失败：%v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
}

func descriptionOf(t *testing.T, db *gorm.DB, userID string) string {
	t.Helper()
	var got string
	if err := db.Raw(`SELECT COALESCE(description, '') FROM profiles WHERE user_id = ?`, userID).Scan(&got).Error; err != nil {
		t.Fatalf("查询描述失败：%v", err)
	}
	return got
}
