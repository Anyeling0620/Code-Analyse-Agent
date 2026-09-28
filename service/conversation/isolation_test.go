package conversation

import (
	"context"
	"testing"

	"edu.agent.code/adaptor/repo/profile"
	"edu.agent.code/adaptor/repo/session"
	"edu.agent.code/service/do"
	"edu.agent.code/service/dto"
)

// fakeSessions 只实现隔离逻辑用到的读取能力，未覆盖的方法沿用内嵌 nil 接口。
type fakeSessions struct {
	session.ISession
	byID map[string]*do.SessionContext
}

func (f *fakeSessions) GetByID(_ context.Context, sessionID string) (*do.SessionContext, error) {
	return f.byID[sessionID], nil
}

// fakeProfiles 用内存 map 模拟按 user_id 存储的画像表。
type fakeProfiles struct {
	profile.IProfile
	rows map[string]*do.Profile
}

func (f *fakeProfiles) GetByUserID(_ context.Context, userID string) (*do.Profile, error) {
	return f.rows[userID], nil
}

func (f *fakeProfiles) Upsert(_ context.Context, p *do.Profile) error {
	saved := *p
	f.rows[p.UserID] = &saved
	return nil
}

func newTestService(sessions *fakeSessions, profiles *fakeProfiles) *Service {
	return &Service{sessions: sessions, profiles: profiles}
}

func TestPrepareSessionKeepsOwnSession(t *testing.T) {
	sessions := &fakeSessions{byID: map[string]*do.SessionContext{
		"s1": {SessionID: "s1", UserID: "Anyeling"},
	}}
	svc := newTestService(sessions, &fakeProfiles{rows: map[string]*do.Profile{}})

	got, err := svc.prepareSession(context.Background(), "Anyeling", "s1")
	if err != nil {
		t.Fatalf("prepareSession: %v", err)
	}
	if got.SessionID != "s1" || got.UserID != "Anyeling" {
		t.Fatalf("本人会话应原样沿用，实际 session=%s owner=%s", got.SessionID, got.UserID)
	}
}

func TestPrepareSessionRejectsForeignSession(t *testing.T) {
	sessions := &fakeSessions{byID: map[string]*do.SessionContext{
		"s1": {SessionID: "s1", UserID: "Anyeling"},
	}}
	svc := newTestService(sessions, &fakeProfiles{rows: map[string]*do.Profile{}})

	got, err := svc.prepareSession(context.Background(), "visitor", "s1")
	if err != nil {
		t.Fatalf("prepareSession: %v", err)
	}
	if got.SessionID == "s1" {
		t.Fatal("他人会话不得被沿用，否则消息会写进别人的会话")
	}
	if got.UserID != "visitor" || got.SessionID == "" {
		t.Fatalf("应新建归属当前用户的会话，实际 session=%s owner=%s", got.SessionID, got.UserID)
	}
}

func TestPrepareSessionCreatesNewWhenMissing(t *testing.T) {
	svc := newTestService(
		&fakeSessions{byID: map[string]*do.SessionContext{}},
		&fakeProfiles{rows: map[string]*do.Profile{}},
	)

	got, err := svc.prepareSession(context.Background(), "visitor", "")
	if err != nil {
		t.Fatalf("prepareSession: %v", err)
	}
	if got.SessionID == "" || got.UserID != "visitor" {
		t.Fatalf("无 session_id 时应新建归属当前用户的会话，实际 session=%s owner=%s", got.SessionID, got.UserID)
	}
}

func TestPrepareProfileStaysPerUser(t *testing.T) {
	svc := newTestService(&fakeSessions{}, &fakeProfiles{rows: map[string]*do.Profile{}})

	if _, err := svc.prepareProfile(context.Background(), "Anyeling", &dto.Profile{
		UserType: "student", SkillLevel: "入门", CurrentTopic: "Eino",
	}); err != nil {
		t.Fatalf("prepareProfile(Anyeling): %v", err)
	}

	// 账号 B 在没有提交画像时，不能读到账号 A 的画像。
	got, err := svc.prepareProfile(context.Background(), "visitor", nil)
	if err != nil {
		t.Fatalf("prepareProfile(visitor): %v", err)
	}
	if got.CurrentTopic != "" || got.UserType != "" {
		t.Fatalf("账号 B 不应拿到账号 A 的画像，实际 %+v", got)
	}
	if got.UserID != "visitor" {
		t.Fatalf("画像归属必须是当前用户，实际 %s", got.UserID)
	}

	// 账号 A 自己已保存的画像应能读回。
	again, err := svc.prepareProfile(context.Background(), "Anyeling", nil)
	if err != nil {
		t.Fatalf("prepareProfile(Anyeling, nil): %v", err)
	}
	if again.CurrentTopic != "Eino" || again.UserID != "Anyeling" {
		t.Fatalf("账号 A 的画像应能读回，实际 %+v", again)
	}
}

func TestPrepareProfileIgnoresSpoofedUserID(t *testing.T) {
	profiles := &fakeProfiles{rows: map[string]*do.Profile{}}
	svc := newTestService(&fakeSessions{}, profiles)

	// 请求体里谎称自己属于 Anyeling，落库仍必须归属当前登录的 visitor。
	if _, err := svc.prepareProfile(context.Background(), "visitor", &dto.Profile{
		UserID: "Anyeling", CurrentTopic: "伪造",
	}); err != nil {
		t.Fatalf("prepareProfile: %v", err)
	}
	if _, ok := profiles.rows["Anyeling"]; ok {
		t.Fatal("不得通过请求体里的 user_id 写入他人画像")
	}
	if _, ok := profiles.rows["visitor"]; !ok {
		t.Fatal("画像应写入当前登录用户")
	}
}
