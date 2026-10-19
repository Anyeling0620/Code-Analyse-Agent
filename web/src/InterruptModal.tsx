/**
 * InterruptModal · terminal 人机确认弹窗
 *
 * 当 SSE 收到 type=="interrupt" 事件时展示：显示 Agent 想执行的命令、风险等级、原因，
 * 用户点 Approve / Reject 后回调 onResolve（POST /api/chat/resume），父组件接管后续流。
 */
import { useState } from 'react';

export interface InterruptEvent {
  pending_approval_id: string;
  pending_command: string;
  pending_risk_reason?: string;
  pending_risk_level?: string;
  pending_workdir?: string;
  pending_timeout_sec?: number;
  session_id?: string;
}

interface Props {
  event: InterruptEvent;
  onResolve: (approved: boolean) => void;
}

export function InterruptModal({ event, onResolve }: Props) {
  const [working, setWorking] = useState(false);

  const handle = (approved: boolean) => {
    if (working) return;
    setWorking(true);
    onResolve(approved);
  };

  const riskLabel =
    event.pending_risk_level === 'destructive'
      ? '破坏性命令'
      : event.pending_risk_level === 'write'
      ? '写入命令'
      : event.pending_risk_level ?? '未知风险';

  return (
    <div className="modal-backdrop" role="dialog" aria-modal="true" aria-labelledby="approval-title">
      <section className="modal-card approval-modal">
        <span className="modal-tab" style={{ background: 'var(--vermilion)', color: 'var(--on-vermilion)' }} aria-hidden="true">
          APPROVAL
        </span>

        <header className="modal-header">
          <div>
            <h2 id="approval-title">需要确认是否执行这条命令</h2>
            <p>
              Agent 想运行下面这条命令；它被识别为 <strong>{riskLabel}</strong>，需要你显式批准。
            </p>
          </div>
          <span className="risk-stamp">{riskLabel}</span>
        </header>

        <pre className="approval-command">{event.pending_command}</pre>

        <div className="approval-meta">
          {event.pending_workdir && <span>工作目录：{event.pending_workdir || '(默认)'}</span>}
          {event.pending_timeout_sec ? <span>超时：{event.pending_timeout_sec}s</span> : null}
          {event.pending_risk_reason && <span>原因：{event.pending_risk_reason}</span>}
        </div>

        <footer className="modal-footer">
          <button className="ghost-button" disabled={working} onClick={() => handle(false)} type="button">
            拒绝
          </button>
          <button className="danger-button" disabled={working} onClick={() => handle(true)} type="button">
            {working ? '执行中…' : '批准并执行'}
          </button>
        </footer>
      </section>
    </div>
  );
}
