import type { StreamPayload } from '../types/chat';

// SSEEventMeta 携带帧级的元信息。目前只有 id：服务端为每条可回放事件写的
// `id: <seq>`，客户端用它作为 Last-Event-ID 断线续传的游标。
export type SSEEventMeta = { id?: string };

export type SSEEventHandler = (eventName: string, payload: StreamPayload, meta: SSEEventMeta) => void;

export async function consumeSSE(stream: ReadableStream<Uint8Array>, onEvent: SSEEventHandler) {
  const reader = stream.getReader();
  const decoder = new TextDecoder();
  let buffer = '';

  while (true) {
    const { done, value } = await reader.read();
    if (done) {
      buffer += decoder.decode();
      break;
    }
    buffer += decoder.decode(value, { stream: true });
    buffer = flushBufferedEvents(buffer, onEvent);
  }

  if (buffer.length > 0) {
    flushBufferedEvents(`${buffer}\n\n`, onEvent);
  }
}

function flushBufferedEvents(buffer: string, onEvent: SSEEventHandler) {
  let rest = buffer;

  while (true) {
    const boundaryMatch = /\r?\n\r?\n/.exec(rest);
    if (!boundaryMatch || typeof boundaryMatch.index !== 'number') {
      return rest;
    }

    const boundary = boundaryMatch.index;
    const rawBlock = rest.slice(0, boundary);
    rest = rest.slice(boundary + boundaryMatch[0].length);
    if (!rawBlock) {
      continue;
    }

    const parsed = parseSSEBlock(rawBlock);
    if (parsed) {
      onEvent(parsed.eventName, parsed.payload, parsed.meta);
    }
  }
}

function parseSSEBlock(rawBlock: string) {
  const lines = rawBlock.split(/\r?\n/);
  let eventName = 'message';
  let lastId: string | undefined;
  const dataLines: string[] = [];

  for (const line of lines) {
    if (line.startsWith('id:')) {
      // 同一帧内出现多个 id 时以最后一个为准（SSE 规范的 last event id 语义）。
      const value = line.slice(3).trim();
      if (value) {
        lastId = value;
      }
      continue;
    }
    if (line.startsWith('event:')) {
      eventName = line.slice(6).trim();
      continue;
    }
    if (line.startsWith('data:')) {
      let data = line.slice(5);
      if (data.startsWith(' ')) {
        data = data.slice(1);
      }
      dataLines.push(data);
    }
  }

  if (dataLines.length === 0) {
    return null;
  }

  try {
    return {
      eventName,
      payload: JSON.parse(dataLines.join('\n')) as StreamPayload,
      meta: lastId ? { id: lastId } : {},
    };
  } catch {
    return null;
  }
}
