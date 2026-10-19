package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"edu.agent.code/adaptor/repo/profile"
	"edu.agent.code/common"
	"edu.agent.code/service/do"
	"edu.agent.code/service/dto"

	"github.com/gin-gonic/gin"
)

type historyTestProfiles struct {
	profile.IProfile
	gotUserID string
	gotLimit  int
	gotOffset int
	rows      []do.ProfileRevision
}

func (f *historyTestProfiles) ListRevisions(_ context.Context, userID string, limit, offset int) ([]do.ProfileRevision, error) {
	f.gotUserID = userID
	f.gotLimit = limit
	f.gotOffset = offset
	return f.rows, nil
}

// GET /api/profile/history 只能读取登录用户自己的快照，响应里回显实际生效的分页参数。
func TestGetProfileHistoryUsesLoginUserAndClampsLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &historyTestProfiles{rows: []do.ProfileRevision{{
		ID:           7,
		UserID:       "u1",
		CurrentTopic: "Eino",
		Source:       do.ProfileSourceAPI,
		UpdatedAt:    time.Now(),
	}}}
	handler := &Handler{profiles: repo}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest("GET", "/api/profile/history?limit=500&offset=2", nil)
	ctx.Set(common.CtxKeyAuthUser, &common.UserInfo{UserID: "u1"})

	handler.GetProfileHistory(ctx)

	if repo.gotUserID != "u1" {
		t.Fatalf("历史查询归属必须是登录用户，实际 %q", repo.gotUserID)
	}
	if repo.gotLimit != profile.MaxRevisionLimit {
		t.Fatalf("limit 应被钳制到 %d，实际 %d", profile.MaxRevisionLimit, repo.gotLimit)
	}
	if repo.gotOffset != 2 {
		t.Fatalf("offset 未透传，实际 %d", repo.gotOffset)
	}

	var response dto.Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if response.Code != common.OK.Code {
		t.Fatalf("响应码不符：%+v", response)
	}
}
