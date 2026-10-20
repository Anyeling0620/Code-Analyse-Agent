package conversation

import (
	"edu.agent.code/service/do"
	"edu.agent.code/service/dto"
)

// renderEventOps 把 do/dto 两套事件结构的字段读写抽象出来，让压缩逻辑只写一份。
// 两套结构字段名相同，但 Timestamp 类型不同（int64 vs string），不能用简单类型转换。
type renderEventOps[T any] struct {
	typ        func(T) string
	visibility func(T) string
	renderMode func(T) string
	delta      func(T) string
	message    func(T) string
	detail     func(T) string
	toolName   func(T) string
	toolResult func(T) string
	stage      func(T) string
	addDelta   func(*T, string)
}

var doRenderEventOps = renderEventOps[do.ChatStreamEvent]{
	typ:        func(e do.ChatStreamEvent) string { return e.Type },
	visibility: func(e do.ChatStreamEvent) string { return e.Visibility },
	renderMode: func(e do.ChatStreamEvent) string { return e.RenderMode },
	delta:      func(e do.ChatStreamEvent) string { return e.Delta },
	message:    func(e do.ChatStreamEvent) string { return e.Message },
	detail:     func(e do.ChatStreamEvent) string { return e.Detail },
	toolName:   func(e do.ChatStreamEvent) string { return e.ToolName },
	toolResult: func(e do.ChatStreamEvent) string { return e.ToolResult },
	stage:      func(e do.ChatStreamEvent) string { return e.Stage },
	addDelta:   func(e *do.ChatStreamEvent, delta string) { e.Delta += delta },
}

var dtoRenderEventOps = renderEventOps[dto.ChatStreamEvent]{
	typ:        func(e dto.ChatStreamEvent) string { return e.Type },
	visibility: func(e dto.ChatStreamEvent) string { return e.Visibility },
	renderMode: func(e dto.ChatStreamEvent) string { return e.RenderMode },
	delta:      func(e dto.ChatStreamEvent) string { return e.Delta },
	message:    func(e dto.ChatStreamEvent) string { return e.Message },
	detail:     func(e dto.ChatStreamEvent) string { return e.Detail },
	toolName:   func(e dto.ChatStreamEvent) string { return e.ToolName },
	toolResult: func(e dto.ChatStreamEvent) string { return e.ToolResult },
	stage:      func(e dto.ChatStreamEvent) string { return e.Stage },
	addDelta:   func(e *dto.ChatStreamEvent, delta string) { e.Delta += delta },
}

// compactRenderEvents 把落库的完整 render_events 压成“前端回放真正会用到”的事件。
//
// 历史消息里绝大多数事件是模型逐 token 的中间产物：
//   - reason：推理过程，历史回放直接忽略；
//   - 空 progress：没有正文，回放不会生成文本块；
//   - tool_call 的流式参数片段：tool_name 为空，前端 addToolCallWithSegment 直接返回；
//   - 连续 delta：前端 appendStreamingDeltaSegment 本来就会合并成同一个文本块。
//
// 压掉这些事件不改变历史渲染结果，但能把单条消息的事件数从几万降到几百。
// 事件顺序保持不变；只有相邻且 visibility/render_mode 相同的 delta 会被合并。
func compactRenderEvents(events []do.ChatStreamEvent) []do.ChatStreamEvent {
	return compactRenderEventsWith(events, doRenderEventOps)
}

// compactDTOEvents 是 compactRenderEvents 的 dto 版本，供只读分享快照使用。
func compactDTOEvents(events []dto.ChatStreamEvent) []dto.ChatStreamEvent {
	return compactRenderEventsWith(events, dtoRenderEventOps)
}

func compactRenderEventsWith[T any](events []T, ops renderEventOps[T]) []T {
	if len(events) == 0 {
		return nil
	}
	compacted := make([]T, 0, len(events))
	for _, event := range events {
		switch ops.typ(event) {
		case "reason", "ready", "session", "done":
			// 回放不消费这些事件。
			continue
		case "delta":
			if ops.visibility(event) == "internal" || ops.renderMode(event) == "none" || ops.delta(event) == "" {
				continue
			}
			if last := len(compacted) - 1; last >= 0 &&
				ops.typ(compacted[last]) == "delta" &&
				ops.visibility(compacted[last]) == ops.visibility(event) &&
				ops.renderMode(compacted[last]) == ops.renderMode(event) {
				ops.addDelta(&compacted[last], ops.delta(event))
				continue
			}
			compacted = append(compacted, event)
		case "progress":
			content := ops.delta(event)
			if content == "" {
				content = ops.message(event)
			}
			if content == "" {
				content = ops.detail(event)
			}
			if ops.visibility(event) == "internal" || ops.renderMode(event) == "none" || content == "" {
				continue
			}
			compacted = append(compacted, event)
		case "tool_call", "tool_result":
			// 前端按 tool_name 建卡片；没有工具名的流式参数片段会被忽略。
			if ops.toolName(event) == "" {
				continue
			}
			compacted = append(compacted, event)
		case "error":
			if ops.message(event) == "" {
				continue
			}
			compacted = append(compacted, event)
		default:
			// observe/interrupt 等事件量很小，保留给执行链路与审批回放。
			if ops.stage(event) == "" && ops.detail(event) == "" && ops.message(event) == "" &&
				ops.delta(event) == "" && ops.toolName(event) == "" && ops.toolResult(event) == "" {
				continue
			}
			compacted = append(compacted, event)
		}
	}
	return compacted
}

const projectAnalysisTool = "repo_analyzer"

// dtoEventsContainProjectAnalysis 供分享快照复用 repo 层的导出判定。
func dtoEventsContainProjectAnalysis(events []dto.ChatStreamEvent) bool {
	for _, event := range events {
		if event.ToolName == projectAnalysisTool {
			return true
		}
	}
	return false
}
