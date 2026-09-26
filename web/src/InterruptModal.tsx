/**
 * InterruptModal · terminal 人机确认弹窗
 *
 * 当 SSE 收到 type=="interrupt" 事件时展示：显示 Agent 想执行的命令、风险等级、原因，
 * 用户点 Approve / Reject 后回调 onResolve（POST /api/chat/resume），父组件接管后续流。
 *
 * 用法（在 App.tsx）：
 *   const [interruptEvent, setInterruptEvent] = useState<InterruptEvent | null>(null);
 *   ...
 *   case 'interrupt': setInterruptEvent(payload as InterruptEvent); break;
 *   ...
 *   {interruptEvent && (
 *     <InterruptModal event={interruptEvent} onResolve={(approved) => handleResume(interruptEvent, approved)} />
 *   )}
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

const overlayStyle: React.CSSProperties = {
  position: 'fixed',
  inset: 0,
  background: 'rgba(13, 13, 13, 0.42)',
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  zIndex: 1000,
  padding: '24px',
};

const cardStyle: React.CSSProperties = {
  background: '#ffffff',
  borderRadius: '14px',
  padding: '24px 28px',
  maxWidth: '560px',
  width: '100%',
  maxHeight: '50vh',
  display: 'flex',
  flexDirection: 'column',
  overflow: 'hidden',
  boxShadow: '0 24px 48px rgba(0, 0, 0, 0.18)',
  fontFamily: 'system-ui, -apple-system, sans-serif',
};

const titleStyle: React.CSSProperties = {
  margin: 0,
  fontSize: '18px',
  fontWeight: 600,
  color: '#0d0d0d',
};

const subtitleStyle: React.CSSProperties = {
  marginTop: '6px',
  fontSize: '13px',
  color: '#6b6b6b',
};

const blockStyle: React.CSSProperties = {
  marginTop: '16px',
  padding: '12px 14px',
  maxHeight: '22vh',
  overflow: 'auto',
  background: '#f7f7f8',
  borderRadius: '8px',
  border: '1px solid #d9d9e3',
  fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
  fontSize: '13px',
  color: '#0d0d0d',
  whiteSpace: 'pre-wrap',
  wordBreak: 'break-all',
};

const metaRowStyle: React.CSSProperties = {
  marginTop: '12px',
  display: 'flex',
  flexWrap: 'wrap',
  gap: '8px 12px',
  maxHeight: '8vh',
  overflow: 'auto',
  fontSize: '12px',
  color: '#6b6b6b',
};

const buttonRowStyle: React.CSSProperties = {
  marginTop: '24px',
  display: 'flex',
  flexShrink: 0,
  gap: '12px',
  justifyContent: 'flex-end',
};

const rejectBtn: React.CSSProperties = {
  padding: '8px 16px',
  border: '1px solid #d9d9e3',
  background: '#fff',
  color: '#6b6b6b',
  borderRadius: '8px',
  cursor: 'pointer',
  fontSize: '14px',
};

const approveBtn: React.CSSProperties = {
  padding: '8px 16px',
  border: 'none',
  background: '#dc2626',
  color: '#fff',
  borderRadius: '8px',
  cursor: 'pointer',
  fontSize: '14px',
  fontWeight: 600,
};

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
    <div style={overlayStyle} role="dialog" aria-modal="true">
      <div style={cardStyle}>
        <h3 style={titleStyle}>需要你确认是否执行这条命令</h3>
        <p style={subtitleStyle}>
          Agent 想运行下面这条命令；它被识别为 <strong>{riskLabel}</strong>，需要你显式批准。
        </p>

        <div style={blockStyle}>{event.pending_command}</div>

        <div style={metaRowStyle}>
          {event.pending_workdir && <span>工作目录：{event.pending_workdir || '(默认)'}</span>}
          {event.pending_timeout_sec ? <span>超时：{event.pending_timeout_sec}s</span> : null}
          {event.pending_risk_reason && <span>原因：{event.pending_risk_reason}</span>}
        </div>

        <div style={buttonRowStyle}>
          <button style={rejectBtn} disabled={working} onClick={() => handle(false)}>
            拒绝
          </button>
          <button style={approveBtn} disabled={working} onClick={() => handle(true)}>
            {working ? '执行中…' : '批准并执行'}
          </button>
        </div>
      </div>
    </div>
  );
}
