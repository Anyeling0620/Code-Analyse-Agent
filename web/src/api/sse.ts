import type { StreamPayload } from '../types/chat';

export async function consumeSSE(stream: ReadableStream<Uint8Array>, onEvent: (eventName: string, payload: StreamPayload) => void) {
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

function flushBufferedEvents(buffer: string, onEvent: (eventName: string, payload: StreamPayload) => void) {
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
      onEvent(parsed.eventName, parsed.payload);
    }
  }
}

function parseSSEBlock(rawBlock: string) {
  const lines = rawBlock.split(/\r?\n/);
  let eventName = 'message';
  const dataLines: string[] = [];

  for (const line of lines) {
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
    return { eventName, payload: JSON.parse(dataLines.join('\n')) as StreamPayload };
  } catch {
    return null;
  }
}
