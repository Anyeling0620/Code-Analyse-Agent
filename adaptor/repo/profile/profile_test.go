package profile

import (
	"context"
	"path/filepath"
	"testing"

	"edu.agent.code/adaptor"
	"edu.agent.code/adaptor/repo/model"
	"edu.agent.code/config"
	"edu.agent.code/service/do"

	"github.com/milvus-io/milvus/client/v2/milvusclient"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// stubAdaptor 只提供 DB，其余能力返回 nil——这段测试只碰画像仓储。
type stubAdaptor struct{ db *gorm.DB }

func (s *stubAdaptor) GetConfig() *config.Config             { return &config.Config{} }
func (s *stubAdaptor) GetDB() *gorm.DB                       { return s.db }
func (s *stubAdaptor) GetMilvusClient() *milvusclient.Client { return nil }
func (s *stubAdaptor) GetRedis() *redis.Client               { return nil }

var _ adaptor.IAdaptor = (*stubAdaptor)(nil)

// 存量行迁移后 description 是 NULL（旧列 purchased_courses 为空数组时不会产生描述）。
// 读画像必须把 NULL 当空串，而不是报 "converting NULL to string is unsupported"——
// 那会让每轮对话的 prepareProfile 都失败。
func TestGetByUserIDToleratesNullDescription(t *testing.T) {
	repo, db := newTestRepo(t)
	if err := db.Exec(`INSERT INTO profiles (user_id, current_stage) VALUES (?, ?)`, "u_null", "学习中").Error; err != nil {
		t.Fatalf("插入 NULL 描述的行失败：%v", err)
	}

	got, err := repo.GetByUserID(context.Background(), "u_null")
	if err != nil {
		t.Fatalf("读取 NULL 描述不该报错：%v", err)
	}
	if got == nil {
		t.Fatal("应当读到画像")
	}
	if got.Description != "" {
		t.Fatalf("NULL 描述应当是空串，实际 %q", got.Description)
	}
	if got.CurrentStage != "学习中" {
		t.Fatalf("其它字段应当正常读回，实际 %q", got.CurrentStage)
	}
}

// 描述要能写能读（Upsert → GetByUserID 往返），并且不存在的用户返回 nil。
func TestUpsertAndReadDescription(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()

	if err := repo.Upsert(ctx, &do.Profile{
		UserID:       "u_write",
		UserType:     "student",
		SkillLevel:   "入门",
		GoalType:     "做项目",
		Description:  "本科在读，正在用 Go + Eino 做 Agent 项目",
		CurrentTopic: "Go Agent Eino 实战",
		CurrentStage: "开发中",
	}, do.ProfileSourceAPI); err != nil {
		t.Fatalf("写入失败：%v", err)
	}

	got, err := repo.GetByUserID(ctx, "u_write")
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if got == nil || got.Description != "本科在读，正在用 Go + Eino 做 Agent 项目" {
		t.Fatalf("描述未正确往返：%+v", got)
	}

	missing, err := repo.GetByUserID(ctx, "u_not_exists")
	if err != nil {
		t.Fatalf("查不存在的用户不该报错：%v", err)
	}
	if missing != nil {
		t.Fatalf("不存在的用户应当返回 nil，实际 %+v", missing)
	}
}

// 已存在的描述不能被空值静默覆盖（Upsert 是全量写，调用方保证传的是完整画像）。
func TestUpsertOverwritesDescriptionWhenProvided(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()

	for _, description := range []string{"第一版描述", "第二版描述"} {
		if err := repo.Upsert(ctx, &do.Profile{UserID: "u_update", Description: description}, do.ProfileSourceChat); err != nil {
			t.Fatalf("写入 %q 失败：%v", description, err)
		}
	}
	got, err := repo.GetByUserID(ctx, "u_update")
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if got.Description != "第二版描述" {
		t.Fatalf("描述未被更新：%q", got.Description)
	}
}

// 每次实际变更都要在同一事务里留下快照；内容没变则既不刷新 updated_at，
// 也不产生重复快照（前端每轮对话都会带完整画像）。
func TestUpsertWritesRevisionOnlyOnChange(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()
	profile := &do.Profile{
		UserID:       "u_rev",
		UserType:     "student",
		SkillLevel:   "入门",
		CurrentTopic: "Eino",
	}

	if err := repo.Upsert(ctx, profile, do.ProfileSourceAPI); err != nil {
		t.Fatalf("首次写入失败：%v", err)
	}
	if profile.UpdatedAt.IsZero() {
		t.Fatal("首次写入后应把实际 updated_at 回填给调用方")
	}
	firstUpdatedAt := profile.UpdatedAt
	// 完全相同的内容再写一次：不产生新快照，也不刷新 updated_at。
	if err := repo.Upsert(ctx, profile, do.ProfileSourceChat); err != nil {
		t.Fatalf("重复写入失败：%v", err)
	}
	if !profile.UpdatedAt.Equal(firstUpdatedAt) {
		t.Fatalf("内容未变化时 updated_at 不该变化：%v -> %v", firstUpdatedAt, profile.UpdatedAt)
	}
	revisions, err := repo.ListRevisions(ctx, "u_rev", 10, 0)
	if err != nil {
		t.Fatalf("读取快照失败：%v", err)
	}
	if len(revisions) != 1 {
		t.Fatalf("内容未变化时不该新增快照，实际 %d 条", len(revisions))
	}
	if revisions[0].Source != do.ProfileSourceAPI || revisions[0].CurrentTopic != "Eino" {
		t.Fatalf("首条快照内容不符：%+v", revisions[0])
	}

	// 改一个字段：新增一条 chat 来源快照，并保留旧值。
	changed := *profile
	changed.CurrentTopic = "Eino 2"
	if err := repo.Upsert(ctx, &changed, do.ProfileSourceChat); err != nil {
		t.Fatalf("变更写入失败：%v", err)
	}
	revisions, err = repo.ListRevisions(ctx, "u_rev", 10, 0)
	if err != nil {
		t.Fatalf("读取快照失败：%v", err)
	}
	if len(revisions) != 2 {
		t.Fatalf("变更后应有 2 条快照，实际 %d 条", len(revisions))
	}
	if revisions[0].CurrentTopic != "Eino 2" || revisions[0].Source != do.ProfileSourceChat {
		t.Fatalf("最新快照应为 chat 来源且是新值：%+v", revisions[0])
	}
	if revisions[1].CurrentTopic != "Eino" {
		t.Fatalf("旧快照应保留历史值：%+v", revisions[1])
	}
}

// 历史快照按用户隔离、按时间倒序，并支持 limit/offset 分页。
func TestListRevisionsScopesUserAndPaginates(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()

	for _, topic := range []string{"t1", "t2", "t3"} {
		if err := repo.Upsert(ctx, &do.Profile{UserID: "u_page", CurrentTopic: topic}, do.ProfileSourceAPI); err != nil {
			t.Fatalf("写入 %q 失败：%v", topic, err)
		}
	}
	if err := repo.Upsert(ctx, &do.Profile{UserID: "u_other", CurrentTopic: "other"}, do.ProfileSourceAPI); err != nil {
		t.Fatalf("写入他人画像失败：%v", err)
	}

	first, err := repo.ListRevisions(ctx, "u_page", 2, 0)
	if err != nil {
		t.Fatalf("第一页失败：%v", err)
	}
	if len(first) != 2 || first[0].CurrentTopic != "t3" || first[1].CurrentTopic != "t2" {
		t.Fatalf("第一页顺序/条数不符：%+v", first)
	}
	second, err := repo.ListRevisions(ctx, "u_page", 2, 2)
	if err != nil {
		t.Fatalf("第二页失败：%v", err)
	}
	if len(second) != 1 || second[0].CurrentTopic != "t1" {
		t.Fatalf("第二页结果不符：%+v", second)
	}
	other, err := repo.ListRevisions(ctx, "u_other", 10, 0)
	if err != nil {
		t.Fatalf("读取他人快照失败：%v", err)
	}
	if len(other) != 1 || other[0].UserID != "u_other" {
		t.Fatalf("快照未按用户隔离：%+v", other)
	}
}

func newTestRepo(t *testing.T) (*Profile, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "profile.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试库失败：%v", err)
	}
	if err := db.AutoMigrate(&model.Profile{}, &model.ProfileRevision{}); err != nil {
		t.Fatalf("建表失败：%v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("取底层连接失败：%v", err)
	}
	// Windows 上句柄不释放会让 TempDir 清理失败：在 TempDir 被删之前先关连接。
	t.Cleanup(func() { _ = sqlDB.Close() })
	return NewProfile(&stubAdaptor{db: db}), db
}
