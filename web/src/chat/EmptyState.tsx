import { FolderIcon, TerminalIcon } from '../ui/icons';

const capabilities = [
  {
    title: '仓库分析',
    body: '自动拉取 Git 仓库或挂载本机项目，建立项目级语义索引，产出固定八节的结构化报告与 Mermaid 架构图。',
    icon: FolderIcon,
    hue: 'var(--section-oxide)',
    onHue: 'var(--on-oxide)',
  },
  {
    title: '项目问答',
    body: '围绕当前项目连续追问：某个接口在哪实现、配置怎样加载、调用链如何串联。',
    icon: TerminalIcon,
    hue: 'var(--section-teal)',
    onHue: 'var(--on-teal)',
  },
];

export function EmptyState() {
  return (
    <section className="empty-state" aria-label="开始一次分析">
      <div className="empty-sheet">
        <span className="empty-tab">START</span>
        <div className="empty-punch empty-punch-top" aria-hidden="true" />
        <div className="empty-punch empty-punch-bottom" aria-hidden="true" />

        <div className="empty-heading">
          <h2>把仓库变成一张可追问的分析页</h2>
          <p>
            接入一个仓库或本机项目路径。Repo Agent 会建立项目级语义索引，调用工具读取代码，
            并把结论、执行证据与继续追问的入口留在同一页上。
          </p>
        </div>

        <div className="empty-index">
          {capabilities.map((item) => {
            const CapabilityIcon = item.icon;
            return (
              <div className="empty-index-row" key={item.title}>
                <span
                  className="empty-index-tab"
                  style={{ background: item.hue, color: item.onHue }}
                  aria-hidden="true"
                >
                  <CapabilityIcon size={16} />
                </span>
                <div className="empty-index-copy">
                  <h3>{item.title}</h3>
                  <p>{item.body}</p>
                </div>
              </div>
            );
          })}
        </div>

        <p className="empty-start">
          在下方输入仓库地址或问题开始。长任务断线后会自动续传，高危命令会先停下来等你确认。
        </p>
      </div>
    </section>
  );
}
