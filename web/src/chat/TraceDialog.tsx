import { memo, useState, useCallback } from 'react';
import { formatTraceDetail } from './formatters';
import type { TraceEvent } from '../types/chat';
import { ActivityIcon, CloseIcon } from '../ui/icons';

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
          <ActivityIcon size={13} />
          执行链路 · {events.length}
        </button>
        {open && (
            <div className="modal-backdrop" role="presentation" onClick={() => setOpen(false)}>
              <section className="modal-card trace-modal" role="dialog" aria-modal="true" aria-label="执行链路详情" onClick={(event) => event.stopPropagation()}>
                <header className="modal-header trace-modal-header">
                  <div>
                    <h2>执行链路</h2>
                    <p>共 {events.length} 条事件 · 双击任意行可复制</p>
                  </div>
                  <div className="trace-modal-actions">
                    <span className="trace-modal-id">TRACE</span>
                    <button type="button" className="ghost-button" onClick={() => setOpen(false)} aria-label="关闭执行链路">
                      <CloseIcon size={16} />
                      <span>关闭</span>
                    </button>
                  </div>
                </header>
                <div className="trace-timeline">
                  {events.map((event, index) => (
                      <div key={event.id} className="trace-row">
                        <span className="trace-index" aria-hidden="true">{String(index + 1).padStart(2, '0')}</span>
                        <strong
                            className="trace-stage"
                            title="双击复制"
                            onDoubleClick={() => handleCopy(event.stage, `${event.id}:stage`)}
                        >
                          {copiedId === `${event.id}:stage` ? '已复制' : event.stage}
                        </strong>
                        <span
                            className={copiedId === `${event.id}:detail` ? 'trace-detail copied' : 'trace-detail'}
                            title="双击复制详情"
                            onDoubleClick={() => handleCopy(event.detail || formatTraceDetail(event), `${event.id}:detail`)}
                        >
                          {copiedId === `${event.id}:detail` ? '已复制' : formatTraceDetail(event)}
                        </span>
                        {typeof event.elapsedMs === 'number' && <em className="trace-elapsed">{event.elapsedMs}ms</em>}
                      </div>
                  ))}
                </div>
              </section>
            </div>
        )}
      </>
  );
});
