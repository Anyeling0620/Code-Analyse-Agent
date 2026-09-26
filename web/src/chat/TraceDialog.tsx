import { memo, useState, useCallback } from 'react';
import { formatTraceDetail } from './formatters';
import type { TraceEvent } from '../types/chat';

export const TraceDialog = memo(function TraceDialog({ events }: { events: TraceEvent[] }) {
  const [open, setOpen] = useState(false);
  const [copiedId, setCopiedId] = useState<string | null>(null);

  const handleCopy = useCallback(async (text: string, eventId: string) => {
    try {
      await navigator.clipboard.writeText(text);
      setCopiedId(eventId);
      setTimeout(() => setCopiedId(null), 1200);
    } catch {
      // ignore clipboard errors (e.g. insecure context)
    }
  }, []);

  return (
      <>
        <button type="button" className="tool-chip trace-entry-button" onClick={() => setOpen(true)}>
          执行链路 · {events.length}
        </button>
        {open && (
            <div className="trace-modal-backdrop" role="presentation" onClick={() => setOpen(false)}>
              <section className="trace-modal" role="dialog" aria-modal="true" aria-label="执行链路详情" onClick={(event) => event.stopPropagation()}>
                <div className="trace-modal-header">
                  <div>
                    <span className="eyebrow">Trace</span>
                    <h2>执行链路详情</h2>
                  </div>
                  <button type="button" className="ghost-button" onClick={() => setOpen(false)}>
                    关闭
                  </button>
                </div>
                <div className="trace-timeline">
                  {events.map((event) => (
                      <div key={event.id} className="trace-row">
                        <strong
                          className="trace-stage"
                          title="双击复制"
                          onDoubleClick={() => handleCopy(event.stage, `${event.id}:stage`)}
                        >
                          {copiedId === `${event.id}:stage` ? '已复制 ✓' : event.stage}
                        </strong>
                        <span
                          className={copiedId === `${event.id}:detail` ? 'trace-detail copied' : 'trace-detail'}
                          title="双击复制详情"
                          onDoubleClick={() => handleCopy(event.detail || formatTraceDetail(event), `${event.id}:detail`)}
                        >
                          {copiedId === `${event.id}:detail` ? '已复制 ✓' : formatTraceDetail(event)}
                        </span>
                        {typeof event.elapsedMs === 'number' && <em>{event.elapsedMs}ms</em>}
                      </div>
                  ))}
                </div>
              </section>
            </div>
        )}
      </>
  );
});
