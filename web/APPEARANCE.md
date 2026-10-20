# 工作台外观系统（布局与主题）

## 入口

工作区顶栏最右侧的 **「外观」** 按钮（`AppearanceSwitcher`，调色盘图标）是唯一入口。
点开后是一个面板，包含两个互相独立的轴：

- **布局 / LAYOUT**：选择整套 UI 世界。当前有：
  - `classic` 经典手册 —— 原有线上界面；
  - `nocturne` 深空控制台 —— 深色任务控制台，会话索引在右；
  - `editorial` 纸本社论 —— 暖白纸面与衬线刊头；
  - `schematic` 蓝图工坊 —— 工程图纸网格与制图标注。
- **主题 / THEME**：浅色、深色、跟随系统。每套新设计都必须同时提供两套主题。

选择会写入 `localStorage` 的 `repo-agent.appearance.v1`，刷新后保持。
切换时在 `<html>` 上写两个属性，样式系统只依赖这两个属性：

```html
<html data-ui="nocturne" data-theme="dark" style="color-scheme: dark">
```

## 预览与验收

- `?ui=nocturne&theme=dark`：直接以指定布局/主题打开，适合截图与分享预览。
- 三套新设计在 `src/theme/designs/<id>.css`，由 `src/main.tsx` 按顺序引入；
  共享的切换器样式在 `src/theme/appearance.css`。

## 新增一套设计的契约

1. 在 `src/theme/appearance.ts` 的 `DESIGNS` 里注册 id、名称、描述与三个色板。
2. 新建 `src/theme/designs/<id>.css`，并在 `src/main.tsx` 中引入。
3. 所有规则必须以 `html[data-ui='<id>']` 作用域开头，禁止泄漏到其他世界。
4. 必须同时实现 `[data-theme='light']` 与 `[data-theme='dark']`，不能只做反色。
5. 保持 `.appearance-control` / `.appearance-trigger` 可见可点；不要裁剪
   `.stage-bar` 的溢出，否则切换面板会被裁掉。
6. DOM 是共享的，重排只能用 CSS（grid/flex/order/position/伪元素）。
7. 响应式基线在 `src/styles.css` 末尾：`<=900px` 时索引板变成抽屉；
   若把索引移到右侧，必须同步覆盖抽屉方向与遮罩。
