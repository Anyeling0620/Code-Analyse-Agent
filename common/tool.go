package common

import (
	"context"
	"github.com/google/uuid"
	"strings"
)

func GetUUIDHex() string {
	return strings.ReplaceAll(uuid.New().String(), "-", "")
}

type checkPointIDKey struct {
}

func WithCheckPointID(ctx context.Context, checkPointID string) context.Context {
	return context.WithValue(ctx, checkPointIDKey{}, checkPointID)
}

// CheckpointIDFromContext 从 Go context 读取 Eino checkpoint id。
func CheckpointIDFromContext(ctx context.Context) string {
	value, _ := ctx.Value(checkPointIDKey{}).(string)
	return value
}
