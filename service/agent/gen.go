package agent

import (
	"context"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/prompt"
)

func GetGenModelInputFunc(template *prompt.DefaultChatTemplate) func(ctx context.Context, _ string, input *adk.AgentInput) ([]adk.Message, error) {
	return func(ctx context.Context, _ string, input *adk.AgentInput) ([]adk.Message, error) {
		var history []adk.Message
		if input != nil {
			history = input.Messages
		}
		return template.Format(ctx, map[string]any{
			"history": history,
		})
	}
}
