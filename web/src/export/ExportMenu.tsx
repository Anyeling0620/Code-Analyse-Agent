import { createPortal } from 'react-dom';
import type { ExportFormat } from './exportDocument';

/**
 * 导出格式菜单。
 *
 * 渲染到 body 而不是挂在触发按钮里：消息卡片是带圆角、且处在滚动容器中的元素，
 * 菜单留在卡片内部会被裁掉。锚点取触发按钮的右下角，菜单向右对齐、向下展开。
 */
export function ExportMenu({
  top,
  left,
  onPick,
}: {
  top: number;
  left: number;
  onPick: (format: ExportFormat) => void;
}) {
  return createPortal(
      <div className="export-menu" style={{ top, left }}>
        <button type="button" onClick={() => onPick('md')}>
          导出 .md
        </button>
        <button type="button" onClick={() => onPick('pdf')}>
          导出 PDF
        </button>
      </div>,
      document.body,
  );
}
