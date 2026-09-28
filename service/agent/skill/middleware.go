package skill

import (
	"context"
	"os"
	"path/filepath"

	"edu.agent.code/common"
	"edu.agent.code/config"
	"edu.agent.code/utils/logger"
	"github.com/cloudwego/eino/adk"
	einoskill "github.com/cloudwego/eino/adk/middlewares/skill"
)

const (
	DefaultToolName = "skill"
)

func defaultDirectories() []string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	return []string{filepath.Join(home, ".cluade", "skills")}
}

func BuildMiddleware(ctx context.Context, conf config.Skills) (adk.ChatModelAgentMiddleware, string, error) {
	if !conf.Enabled {
		return nil, "", nil
	}
	directories := conf.Directories
	if len(directories) == 0 {
		directories = defaultDirectories()
	}
	toolName := DefaultToolName
	if conf.ToolName != "" {
		toolName = conf.ToolName
	}
	backend, err := NewDirectoryBackend(ctx, directories)
	if err != nil {
		return nil, "", err
	}
	middleware, err := einoskill.NewMiddleware(ctx, &einoskill.Config{
		Backend:       backend,
		SkillToolName: common.Ptr(toolName),
	})
	if err != nil {
		return nil, "", err
	}
	logger.Info("skills: enabled tool=%s, dirs=%v", toolName, directories)
	return middleware, toolName, nil
}
