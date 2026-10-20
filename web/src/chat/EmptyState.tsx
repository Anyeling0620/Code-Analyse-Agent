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

/** 边注插图：一张回答活页的构造（章节签、装订孔、边轨）。纯装饰，不承载语义。 */
function LeafPlate() {
  return (
    <svg className="empty-plate-figure" viewBox="0 0 184 224" focusable="false">
      <g className="empty-plate-ghost">
        <rect x="20" y="30" width="112" height="168" transform="rotate(-2.4 76 114)" />
      </g>
      <g className="empty-plate-leaf">
        <rect className="empty-plate-sheet" x="36" y="20" width="116" height="174" />
        <rect className="empty-plate-punch" x="44.4" y="44.4" width="7.2" height="7.2" rx="3.6" />
        <rect className="empty-plate-punch" x="44.4" y="160.4" width="7.2" height="7.2" rx="3.6" />
        <line className="empty-plate-title" x1="62" y1="44" x2="134" y2="44" />
        <line className="empty-plate-title empty-plate-title-soft" x1="62" y1="57" x2="112" y2="57" />
        <g className="empty-plate-chart">
          <line x1="62" y1="72" x2="62" y2="108" />
          <line x1="62" y1="108" x2="134" y2="108" />
          <polyline points="66,100 80,90 92,94 106,78 120,84 132,73" />
          <circle className="empty-plate-dot" cx="132" cy="73" r="2.4" />
        </g>
        <line className="empty-plate-line" x1="62" y1="128" x2="134" y2="128" />
        <line className="empty-plate-line" x1="62" y1="140" x2="120" y2="140" />
        <line className="empty-plate-line" x1="62" y1="152" x2="127" y2="152" />
        <line className="empty-plate-line" x1="62" y1="164" x2="102" y2="164" />
        <line className="empty-plate-line" x1="62" y1="176" x2="90" y2="176" />
      </g>
      <rect className="empty-plate-tab" x="54" y="11" width="44" height="9" />
      <g className="empty-plate-rail">
        <rect x="152" y="74" width="7" height="22" />
        <rect x="152" y="102" width="7" height="16" />
        <rect className="empty-plate-rail-active" x="152" y="124" width="10" height="30" />
      </g>
      <g className="empty-plate-dims">
        <line x1="36" y1="209" x2="152" y2="209" />
        <line x1="36" y1="204" x2="36" y2="214" />
        <line x1="152" y1="204" x2="152" y2="214" />
        <path d="M42 209l5-2.5v5z" />
        <path d="M146 209l-5-2.5v5z" />
      </g>
      <g className="empty-plate-callouts">
        <path d="M98 15h28l8 8" />
        <text x="137" y="26">01</text>
        <path d="M40 48H26" />
        <text x="22" y="51" textAnchor="end">02</text>
        <path d="M162 139h12" />
        <text x="178" y="142" textAnchor="end">03</text>
      </g>
    </svg>
  );
}

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

        <aside className="empty-margin" aria-hidden="true">
          <div className="empty-margin-head">
            <span>PLATE 01</span>
            <span>REF · 00 / 08</span>
          </div>
          <div className="empty-plate">
            <LeafPlate />
            <p className="empty-plate-caption">
              <span className="empty-plate-fig">FIG. 01</span>
              <span className="empty-plate-caption-text">一张回答的构造</span>
            </p>
          </div>
          <ul className="empty-plate-key">
            <li>
              <span className="empty-key-mark empty-key-mark-tab" />
              章节签 · TAB
            </li>
            <li>
              <span className="empty-key-mark empty-key-mark-punch" />
              装订孔 · PUNCH
            </li>
            <li>
              <span className="empty-key-mark empty-key-mark-rail" />
              边轨 · RAIL
            </li>
          </ul>
          <div className="empty-margin-foot">
            <span className="empty-stamp">
              <span className="empty-stamp-title">LIVE REFERENCE</span>
              <span className="empty-stamp-sub">活页参考 · VOL. 01</span>
            </span>
          </div>
        </aside>
      </div>
    </section>
  );
}
