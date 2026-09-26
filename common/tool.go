package common

import (
	"context"
	"github.com/google/uuid"
	"strings"
)

type userIDKey struct{}
type sessionIDKey struct{}
type checkPointIDKey struct{}

func GetUUIDHex() string {
	return strings.ReplaceAll(uuid.New().String(), "-", "")
}

func WithCheckPointID(ctx context.Context, checkPointID string) context.Context {
	return context.WithValue(ctx, checkPointIDKey{}, checkPointID)
}

// CheckpointIDFromContext 从 Go context 读取 Eino checkpoint id。
func CheckpointIDFromContext(ctx context.Context) string {
	value, _ := ctx.Value(checkPointIDKey{}).(string)
	return value
}

func UserIDFromContext(ctx context.Context) string {
	value, _ := ctx.Value(userIDKey{}).(string)
	return value
}

func SessionIDFromContext(ctx context.Context) string {
	value, _ := ctx.Value(sessionIDKey{}).(string)
	return value
}

// WithUserAndSession 把用户和会话 ID 写入 Go context，供工具中间件读取。
func WithUserAndSession(ctx context.Context, userID, sessionID string) context.Context {
	ctx = context.WithValue(ctx, userIDKey{}, userID)
	ctx = context.WithValue(ctx, sessionIDKey{}, sessionID)
	return ctx
}
