import { normalizeMarkdown } from '../markdown/normalizeMarkdown';
import type { ChatMessage, ChatMessageRecord, MessageSegment, StreamPayload, ToolTrace } from '../types/chat';

export function messageRecordToChatMessage(record: ChatMessageRecord): ChatMessage {
  const normalizedRecordContent = record.role === 'assistant' ? normalizeMarkdown(record.content) : record.content;

  const base: ChatMessage = {
    id: `history-${record.id}-${record.role}`,
    role: record.role,
    content: normalizedRecordContent,
    tools: [],
    toolEvents: [],
    segments: [],
    traceEvents: [],
    status: 'done',
  };

  if (record.role !== 'assistant') {
    return base;
  }

  const events = Array.isArray(record.render_events) ? record.render_events : [];
  if (events.length === 0) {
    return {
      ...base,
      segments: isBlankText(normalizedRecordContent) ? [] : [{ type: 'text', id: `history-segment-${record.id}`, content: normalizedRecordContent, source: 'delta' }],
    };
  }

  let replayed = base;
  for (const payload of events) {
    switch (payload.type) {
      case 'progress':
        replayed = appendTextBlockSegment(replayed, payload.delta ?? payload.message ?? payload.detail ?? '', payload, false, 'progress');
        break;
      case 'delta':
        replayed = appendStreamingDeltaSegment(replayed, payload.delta ?? '', payload);
        break;
      case 'tool_call':
        replayed = addToolCallWithSegment(replayed, payload);
        break;
      case 'tool_result':
        replayed = applyToolResultWithSegment(replayed, payload);
        break;
      case 'error':
        replayed = appendErrorSegment(replayed, payload.message ?? '流式请求失败，请稍后再试。');
        break;
      default:
        break;
    }
  }

  const finalContent = normalizeMarkdown(normalizedRecordContent || replayed.content);

  return {
    ...replayed,
    content: finalContent,
    toolEvents: finalizeToolEvents(replayed.toolEvents),
    segments: finalizeAssistantSegments(replayed.segments, finalContent),
    status: 'done',
  };
}

export function getMessageCopyText(message: ChatMessage) {
  if (!isBlankText(message.content)) {
    return message.content;
  }
  return message.segments
      .filter((segment): segment is Extract<MessageSegment, { type: 'text' }> => segment.type === 'text')
      .map((segment) => segment.content)
      .join('\n')
      .trim();
}

export function appendTextBlockSegment(message: ChatMessage, content: string, payload: StreamPayload, includeInFinalContent: boolean, source?: 'progress' | 'delta'): ChatMessage {
  if (payload.visibility === 'internal' || payload.render_mode === 'none' || isBlankText(content)) {
    return message;
  }
  const segments = [...message.segments, { type: 'text' as const, id: crypto.randomUUID(), content, source }];
  return { ...message, content: includeInFinalContent ? message.content + content : message.content, segments };
}

export function appendStreamingDeltaSegment(message: ChatMessage, content: string, payload: StreamPayload): ChatMessage {
  if (payload.visibility === 'internal' || payload.render_mode === 'none' || content === '') {
    return message;
  }

  const segments = [...message.segments];
  const last = segments[segments.length - 1];
  if (last?.type === 'text' && last.source === 'delta') {
    segments[segments.length - 1] = { ...last, content: last.content + content };
    return { ...message, content: message.content + content, segments };
  }

  segments.push({ type: 'text', id: crypto.randomUUID(), content, source: 'delta' });
  return { ...message, content: message.content + content, segments };
}

export function addToolCallWithSegment(message: ChatMessage, payload: StreamPayload): ChatMessage {
  const toolName = payload.tool_name?.trim();
  if (!toolName) {
    return message;
  }

  const existingTrace = findToolTrace(message.toolEvents, payload.tool_call_id, toolName);
  const traceId = existingTrace?.id ?? crypto.randomUUID();
  const callId = payload.tool_call_id ?? existingTrace?.callId ?? traceId;
  const toolArguments = mergeToolArguments(existingTrace?.arguments ?? '', payload.tool_arguments);

  const newTrace: ToolTrace = {
    id: traceId,
    callId,
    name: toolName,
    arguments: toolArguments,
    result: existingTrace?.result ?? '',
    status: 'calling',
  };

  const toolEvents = existingTrace
      ? message.toolEvents.map((item) => (item.id === existingTrace.id ? newTrace : item))
      : [...message.toolEvents, newTrace];

  const segments = [...message.segments];
  const existingSegIdx = segments.findIndex((seg) => seg.type === 'tool' && (seg.tool.callId === callId || seg.tool.id === traceId));
  if (existingSegIdx >= 0) {
    const seg = segments[existingSegIdx] as { type: 'tool'; id: string; tool: ToolTrace };
    segments[existingSegIdx] = { ...seg, tool: { ...seg.tool, name: toolName, callId, arguments: toolArguments, status: 'calling' } };
  } else {
    segments.push({ type: 'tool', id: traceId, tool: newTrace });
  }

  const tools = toolName === 'unknown_tool' || message.tools.includes(toolName) ? message.tools : [...message.tools, toolName];
  return { ...message, tools, toolEvents, segments };
}

export function appendErrorSegment(message: ChatMessage, content: string): ChatMessage {
  if (isBlankText(content)) {
    return message;
  }
  return {
    ...message,
    segments: [...message.segments, { type: 'error' as const, id: crypto.randomUUID(), content }],
  };
}

export function applyToolResultWithSegment(message: ChatMessage, payload: StreamPayload): ChatMessage {
  const toolName = payload.tool_name?.trim();
  if (!toolName) {
    return message;
  }

  const existingTrace = findToolTrace(message.toolEvents, payload.tool_call_id, toolName);
  const callId = payload.tool_call_id ?? existingTrace?.callId ?? '';

  const toolEvents = existingTrace
      ? message.toolEvents.map((item) => (item.id === existingTrace.id ? { ...item, name: toolName, callId, result: payload.tool_result ?? item.result, status: 'done' as const } : item))
      : [...message.toolEvents, { id: crypto.randomUUID(), name: toolName, callId, arguments: payload.tool_arguments ?? '', result: payload.tool_result ?? '', status: 'done' as const }];

  let matchedSegment = false;
  const segments = message.segments.map((seg) => {
    if (seg.type !== 'tool') return seg;
    const match = Boolean(
        (callId && seg.tool.callId === callId) ||
        (existingTrace?.id && seg.tool.id === existingTrace.id) ||
        (!callId && !existingTrace && seg.tool.name === toolName && seg.tool.status === 'calling'),
    );
    if (!match) return seg;
    matchedSegment = true;
    return { ...seg, tool: { ...seg.tool, name: toolName, callId: callId || seg.tool.callId, result: payload.tool_result ?? seg.tool.result, status: 'done' as const } };
  });

  if (!matchedSegment) {
    const trace = toolEvents[toolEvents.length - 1];
    segments.push({ type: 'tool', id: trace.id, tool: trace });
  }

  const tools = toolName === 'unknown_tool' || message.tools.includes(toolName) ? message.tools : [...message.tools, toolName];
  return { ...message, tools, toolEvents, segments };
}

export function finalizeToolEvents(toolEvents: ToolTrace[]) {
  return toolEvents.map((tool) => (tool.status === 'calling' ? { ...tool, status: 'done' as const } : tool));
}

export function finalizeSegments(segments: MessageSegment[]): MessageSegment[] {
  return segments.map((seg) => {
    if (seg.type === 'tool' && seg.tool.status === 'calling') {
      return { ...seg, tool: { ...seg.tool, status: 'done' as const } };
    }
    return seg;
  });
}

function finalizeAssistantSegments(segments: MessageSegment[], finalContent?: string): MessageSegment[] {
  const finalized = finalizeSegments(segments);
  const normalizedFinalContent = normalizeMarkdown(finalContent ?? '');
  const hasTextSegment = finalized.some((seg) => seg.type === 'text' && !isBlankText(seg.content));

  if (hasTextSegment || isBlankText(normalizedFinalContent)) {
    return finalized;
  }

  return [
    ...finalized,
    {
      type: 'text',
      id: crypto.randomUUID(),
      content: normalizedFinalContent,
      source: 'delta',
    },
  ];
}

export function hasMeaningfulAssistantState(message: ChatMessage) {
  return Boolean(!isBlankText(message.content) || message.tools.length > 0 || message.toolEvents.length > 0 || message.segments.some((seg) => seg.type === 'text' && !isBlankText(seg.content)));
}

export function isBlankText(text: string) {
  return text.trim() === '';
}

function findToolTrace(toolEvents: ToolTrace[], callId?: string, toolName?: string) {
  if (callId) {
    const byCallID = toolEvents.find((item) => item.callId === callId);
    if (byCallID) {
      return byCallID;
    }
  }
  if (toolName) {
    return toolEvents.find((item) => item.name === toolName && item.status === 'calling');
  }
  return toolEvents.find((item) => item.status === 'calling');
}

export function hasToolName(payload: StreamPayload) {
  return Boolean(payload.tool_name?.trim());
}

function mergeToolArguments(current: string, incoming?: string) {
  if (incoming === undefined) {
    return current;
  }
  return current + incoming;
}

export function prettyPayload(raw: string) {
  const text = raw.trim();
  if (!text) {
    return '';
  }
  try {
    return JSON.stringify(JSON.parse(text), null, 2);
  } catch {
    return text;
  }
}
