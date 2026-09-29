// 由 eval/compact/hallucination/results.json 生成配套表单
// context_compaction_hallucination_eval.xlsx。
//
// 运行（node_modules 需指向 Codex 运行时依赖目录，ESM 按文件位置解析包）：
//   node <workdir>/build_xlsx.mjs \
//        --results <worktree>/eval/compact/hallucination/results.json \
//        --out <worktree>/eval/compact/hallucination/context_compaction_hallucination_eval.xlsx
//
// 表单结构：
//   审计总览（结论）：压缩概况、摘要文本层信号、审计员计数、探针计数（计数列均为公式，可追溯到明细）
//   探针明细（来源）：真实调用台账、逐探针两臂判定、关键事实保留网格
//   回答原文（证据）：每条探针在“完整历史 / 压缩后上下文”两臂下的模型原话
//   审计员结论（证据）：审计员列出的矛盾 / 编造 / 遗漏原句与理由
import fs from "node:fs/promises";
import { SpreadsheetFile, Workbook } from "@oai/artifact-tool";

const FONT = "Arial";
const BAND_FILL = "#1F4E78";
const HEAD_FILL = "#2F5597";
const RULE = "#B4C6E7";
const LINE = "#D9D9D9";
const LINE_SOFT = "#EDEDED";

function arg(name, fallback) {
  const i = process.argv.indexOf(`--${name}`);
  return i >= 0 && process.argv[i + 1] ? process.argv[i + 1] : fallback;
}

const resultsPath = arg("results", "eval/compact/hallucination/results.json");
const outPath = arg("out", "eval/compact/hallucination/context_compaction_hallucination_eval.xlsx");
const rep = JSON.parse(await fs.readFile(resultsPath, "utf8"));

const tf = (v) => (v ? "TRUE" : "FALSE");
// 时间戳写成带尾注的字符串，避免被表格引擎识别成日期序列号。
const stamp = (iso) =>
  String(iso).replace("T", " ").replace(/([+-]\d{2}:\d{2})$/, " (UTC$1)");
const factOrder = rep.planted_facts.map((f) => f.id);
const factMeta = (id) => rep.planted_facts.find((f) => f.id === id) || {};
const cases = rep.cases;
const probeOf = (c) => c.probes;

// ------------------------------------------------------------------ 工作簿
const workbook = Workbook.create();
const out = workbook.worksheets.add("审计总览");
const detail = workbook.worksheets.add("探针明细");
const raw = workbook.worksheets.add("回答原文");
const judge = workbook.worksheets.add("审计员结论");
for (const s of [out, detail, raw, judge]) s.showGridLines = false;
out.tabColor = BAND_FILL;

const colCount = (letter) => letter.charCodeAt(0) - 64;
const body = (range) => {
  range.format.font = { name: FONT, size: 10 };
  range.format.verticalAlignment = "center";
  range.format.borders = {
    top: { style: "thin", color: LINE },
    bottom: { style: "thin", color: LINE },
    left: { style: "thin", color: LINE },
    right: { style: "thin", color: LINE },
    insideHorizontal: { style: "thin", color: LINE_SOFT },
  };
};
const head = (range) => {
  range.format.fill = HEAD_FILL;
  range.format.font = { name: FONT, size: 10, bold: true, color: "#FFFFFF" };
  range.format.horizontalAlignment = "center";
  range.format.verticalAlignment = "center";
  range.format.wrapText = true;
};
const band = (sheet, row, text, cols) => {
  const span = sheet.getRange(`A${row}:${cols}${row}`);
  span.values = [Array.from({ length: colCount(cols) }, (_, i) => (i === 0 ? text : ""))];
  span.format.fill = BAND_FILL;
  span.format.font = { name: FONT, size: 10, bold: true, color: "#FFFFFF" };
  span.format.verticalAlignment = "center";
  span.format.rowHeight = 20;
};
const note = (sheet, cell, text) => {
  const r = sheet.getRange(cell);
  r.values = [[text]];
  r.format.font = { name: FONT, size: 9, italic: true, color: "#595959" };
  r.format.verticalAlignment = "top";
};
const tableTitle = (sheet, row, text) => {
  const r = sheet.getRange(`A${row}`);
  r.values = [[text]];
  r.format.font = { name: FONT, size: 11, bold: true };
};

// ==========================================================================
// 探针明细：真实调用台账 + 逐探针判定 + 关键事实保留网格
// ==========================================================================
const ledger = [];
let seq = 0;
for (const c of cases) {
  ledger.push([
    ++seq, "压缩摘要", c.name, 1, c.summary_call.prompt_messages, c.summary_call.prompt_estimated_tokens,
    c.summary_call.prompt_tokens, c.summary_call.completion_tokens, c.summary_call.reasoning_tokens,
    c.summary_call.total_tokens, c.summary_call.latency_ms, "压缩摘要调用",
  ]);
}
for (const c of cases) {
  ledger.push([
    ++seq, "审计员", c.name, 1, "", "", c.judge.prompt_tokens, c.judge.completion_tokens,
    c.judge.reasoning_tokens, c.judge.prompt_tokens + c.judge.completion_tokens, c.judge.latency_ms,
    "摘要文本矛盾 / 编造审计",
  ]);
}
const loggedCalls = ledger.reduce((s, r) => s + r[3], 0);
const loggedPrompt = ledger.reduce((s, r) => s + (r[6] || 0), 0);
const loggedComp = ledger.reduce((s, r) => s + (r[7] || 0), 0);
const loggedReason = ledger.reduce((s, r) => s + (r[8] || 0), 0);
ledger.push([
  ++seq, "端到端探针", `${cases.length} 个案例 × 8 探针 × 2 臂`,
  rep.totals.real_model_calls - loggedCalls, "", "",
  rep.totals.prompt_tokens - loggedPrompt,
  rep.totals.completion_tokens - loggedComp,
  rep.totals.reasoning_tokens - loggedReason,
  rep.totals.total_tokens - loggedPrompt - loggedComp,
  "",
  "两臂问答未逐条留痕；reasoning 由总量残差推算",
]);

const ledgerFirst = 3;
const ledgerLast = ledgerFirst + ledger.length - 1;
const totalRef = (col) => `'探针明细'!${col}${ledgerFirst}:${col}${ledgerLast}`;

detail.getRange("A1").values = [["真实模型调用台账"]];
detail.getRange("A1").format.font = { name: FONT, size: 12, bold: true };
detail.getRange("A2:L2").values = [[
  "序号", "阶段", "案例", "调用次数", "输入消息数", "估算输入 tokens",
  "真实 prompt tokens", "真实 completion tokens", "真实 reasoning tokens",
  "真实 total tokens", "延迟 ms", "备注",
]];
head(detail.getRange("A2:L2"));
detail.getRange(`A${ledgerFirst}:L${ledgerLast}`).values = ledger;
body(detail.getRange(`A${ledgerFirst}:L${ledgerLast}`));
detail.getRange(`E${ledgerFirst}:F${ledgerLast}`).format.numberFormat = "#,##0";
detail.getRange(`G${ledgerFirst}:K${ledgerLast}`).format.numberFormat = "#,##0";
detail.getRange(`A${ledgerFirst}:A${ledgerLast}`).format.horizontalAlignment = "center";
detail.getRange(`D${ledgerFirst}:D${ledgerLast}`).format.horizontalAlignment = "center";
detail.getRange(`L${ledgerFirst}:L${ledgerLast}`).format.wrapText = true;

const probeTitle = ledgerLast + 2;
tableTitle(detail, probeTitle, "端到端探针（同模型、同问题，两臂上下文不同；判定口径见「审计总览」）");
detail.getRange(`A${probeTitle + 1}:K${probeTitle + 1}`).values = [[
  "案例", "探针", "类型", "问题", "关键词", "预期", "完整历史判定", "压缩后判定",
  "压缩臂编造标识符", "行类型", "行序号",
]];
head(detail.getRange(`A${probeTitle + 1}:K${probeTitle + 1}`));
const probeRows = [];
for (const c of cases) {
  for (const p of probeOf(c)) {
    probeRows.push([
      c.name, p.id, p.type, p.question, p.keyword || "-", p.expect,
      p.full_verdict, p.compacted_verdict, p.compacted_invented_id || "-", p.type, p.id,
    ]);
  }
}
const probeFirst = probeTitle + 2;
const probeLast = probeFirst + probeRows.length - 1;
detail.getRange(`A${probeFirst}:K${probeLast}`).values = probeRows;
body(detail.getRange(`A${probeFirst}:K${probeLast}`));
detail.getRange(`C${probeFirst}:C${probeLast}`).format.horizontalAlignment = "center";
detail.getRange(`G${probeFirst}:H${probeLast}`).format.horizontalAlignment = "center";
detail.getRange(`D${probeFirst}:D${probeLast}`).format.wrapText = true;
const verdictRef = (col) => `'探针明细'!$${col}$${probeFirst}:$${col}$${probeLast}`;
const typeRef = verdictRef("J");
const caseRef = verdictRef("A");

const gridTitle = probeLast + 2;
tableTitle(detail, gridTitle, "关键事实在压缩摘要文本中的保留情况（TRUE = 摘要里仍含该标识符）");
const caseCols = ["D", "E", "F", "G"];
detail.getRange(`A${gridTitle + 1}:${caseCols[cases.length - 1]}${gridTitle + 1}`).values = [[
  "事实", "类型", "关键词", ...cases.map((c) => c.name),
]];
head(detail.getRange(`A${gridTitle + 1}:${caseCols[cases.length - 1]}${gridTitle + 1}`));
const gridRows = factOrder.map((id) => {
  const m = factMeta(id);
  return [id, m.kind || "", m.keyword || "", ...cases.map((c) => tf(c.summary_keyword_retention[id]))];
});
const gridFirst = gridTitle + 2;
const gridLast = gridFirst + gridRows.length - 1;
detail.getRange(`A${gridFirst}:${caseCols[cases.length - 1]}${gridLast}`).values = gridRows;
body(detail.getRange(`A${gridFirst}:${caseCols[cases.length - 1]}${gridLast}`));
detail.getRange(`D${gridFirst}:${caseCols[cases.length - 1]}${gridLast}`).format.horizontalAlignment = "center";
const gridColRef = (letter) => `'探针明细'!$${letter}$${gridFirst}:$${gridLast}`;

detail.getRange("A:A").format.columnWidth = 14;
detail.getRange("B:B").format.columnWidth = 22;
detail.getRange("C:C").format.columnWidth = 18;
detail.getRange("D:D").format.columnWidth = 40;
detail.getRange("E:E").format.columnWidth = 22;
detail.getRange("F:F").format.columnWidth = 34;
detail.getRange("G:H").format.columnWidth = 13;
detail.getRange("I:I").format.columnWidth = 22;
detail.getRange("J:K").format.columnWidth = 12;
detail.getRange("L:L").format.columnWidth = 30;

// ==========================================================================
// 审计总览
// ==========================================================================
out.getRange("A2").values = [["上下文压缩摘要保真与幻觉审计"]];
out.getRange("A2").format.font = { name: FONT, size: 14, bold: true };
out.getRange("A2").format.rowHeight = 22;
out.getRange("A3:H3").format.borders = { bottom: { style: "thin", color: RULE } };
out.getRange("A4:B8").values = [
  ["工作树", rep.branch],
  ["评估对象", "service/agent/compress 上下文压缩中间件"],
  ["摘要模型", `${rep.model} @ ${rep.base_url}`],
  ["生成时间", stamp(rep.generated_at)],
  ["token 口径", "估算 = runes/4+1（仅用于压缩前后对比）；真实 token 取自模型响应 usage"],
];
out.getRange("A4:B8").format.font = { name: FONT, size: 10 };
out.getRange("A4:A8").format.font = { name: FONT, size: 10, color: "#595959" };
note(out, "L4", rep.verdict_note);

band(out, 11, "1 压缩与摘要文本层（不依赖模型再判断）", "J");
out.getRange("A12:J12").values = [[
  "案例", "轮次 × 单条 runes", "消息数 前 → 后", "估算 token 前", "估算 token 后", "压缩比",
  "摘要 runes", "关键事实保留率", "未知项仍在摘要", "陷阱实体被写进摘要",
]];
head(out.getRange("A12:J12"));
const sec1First = 13;
const sec1Rows = cases.map((c) => [
  c.name, `${c.rounds} × ${c.runes_per_tool_result}`, `${c.before.messages} → ${c.after.messages}`,
  c.before.estimated_tokens, c.after.estimated_tokens, "", c.summary_runes, "",
  tf(c.summary_keeps_unknown_item),
  Object.entries(c.summary_mentions_trap_entity).filter(([, v]) => v).map(([k]) => k).join("、") || "无",
]);
const sec1Last = sec1First + sec1Rows.length - 1;
out.getRange(`A${sec1First}:J${sec1Last}`).values = sec1Rows;
body(out.getRange(`A${sec1First}:J${sec1Last}`));
for (let i = 0; i < sec1Rows.length; i++) {
  out.getRange(`F${sec1First + i}`).formulas = [[
    `=IF(D${sec1First + i}=0,0,1-E${sec1First + i}/D${sec1First + i})`,
  ]];
  // 保留率直接取评测器算出的比例：本引擎对跨表数组公式会返回 #VALUE!，
  // 网格仍逐条列出 TRUE/FALSE 作为证据。
  out.getRange(`H${sec1First + i}`).values = [[cases[i].summary_keyword_retention_pct / 100]];
}
out.getRange(`C${sec1First}:C${sec1Last}`).format.horizontalAlignment = "center";
out.getRange(`D${sec1First}:E${sec1Last}`).format.numberFormat = "#,##0";
out.getRange(`F${sec1First}:F${sec1Last}`).format.numberFormat = "0.0%";
out.getRange(`G${sec1First}:G${sec1Last}`).format.numberFormat = "#,##0";
out.getRange(`H${sec1First}:H${sec1Last}`).format.numberFormat = "0%";
out.getRange(`I${sec1First}:I${sec1Last}`).format.horizontalAlignment = "center";
note(out, "L12", "保留率 = 摘要文本中仍含关键标识符的事实数 ÷ 5；这与「模型还答不答得对」是两件事。");

const sec2 = sec1Last + 2;
band(out, sec2, "2 LLM 审计员：摘要文本的矛盾 / 编造 / 遗漏（依据 = 带工具调用参数的原文转写）", "J");
out.getRange(`A${sec2 + 1}:F${sec2 + 1}`).values = [[
  "案例", "矛盾条数", "编造条数", "遗漏条数", "审计延迟 ms", "审计员 parse 错误",
]];
head(out.getRange(`A${sec2 + 1}:F${sec2 + 1}`));
const sec2First = sec2 + 2;
const sec2Rows = cases.map((c) => [
  c.name, c.judge.contradictions.length, c.judge.fabrications.length,
  c.judge.omissions.length, c.judge.latency_ms, c.judge.parse_error || "无",
]);
const sec2Last = sec2First + sec2Rows.length - 1;
out.getRange(`A${sec2First}:F${sec2Last}`).values = sec2Rows;
body(out.getRange(`A${sec2First}:F${sec2Last}`));
out.getRange(`B${sec2First}:E${sec2Last}`).format.numberFormat = "#,##0";
note(out, `H${sec2 + 1}`, "审计员原始输出见 results.json 的 judge.raw；逐条原句见「审计员结论」页。");

const sec3 = sec2Last + 2;
band(out, sec3, "3 端到端探针计数（计数列为公式，追溯到「探针明细」）", "J");
out.getRange(`A${sec3 + 1}:G${sec3 + 1}`).values = [[
  "案例", "事实答对（压缩臂）", "事实答对（完整臂）", "未知项答对", "陷阱抵抗", "陷阱编造", "压缩臂幻觉总数",
]];
head(out.getRange(`A${sec3 + 1}:G${sec3 + 1}`));
const sec3First = sec3 + 2;
const sec3Last = sec3First + cases.length - 1;
out.getRange(`A${sec3First}:G${sec3Last}`).values = cases.map((c) => [c.name, "", "", "", "", "", ""]);
body(out.getRange(`A${sec3First}:G${sec3Last}`));
for (let i = 0; i < cases.length; i++) {
  const r = sec3First + i;
  const cn = `"${cases[i].name}"`;
  out.getRange(`B${r}`).formulas = [[`=COUNTIFS(${caseRef},${cn},${typeRef},"planted",${verdictRef("H")},"ok")`]];
  out.getRange(`C${r}`).formulas = [[`=COUNTIFS(${caseRef},${cn},${typeRef},"planted",${verdictRef("G")},"ok")`]];
  out.getRange(`D${r}`).formulas = [[`=COUNTIFS(${caseRef},${cn},${typeRef},"unknown",${verdictRef("H")},"ok")`]];
  out.getRange(`E${r}`).formulas = [[`=COUNTIFS(${caseRef},${cn},${typeRef},"trap",${verdictRef("H")},"ok")`]];
  out.getRange(`F${r}`).formulas = [[`=COUNTIFS(${caseRef},${cn},${typeRef},"trap",${verdictRef("H")},"hallucination")`]];
  out.getRange(`G${r}`).formulas = [[`=COUNTIFS(${caseRef},${cn},${verdictRef("H")},"hallucination")`]];
}
out.getRange(`B${sec3First}:G${sec3Last}`).format.numberFormat = "#,##0";
out.getRange(`B${sec3First}:G${sec3Last}`).format.horizontalAlignment = "center";
note(out, `I${sec3 + 1}`, "分母：事实题 5（planted）、未知项 1、陷阱 2；同一行两臂使用完全相同的提法。");
note(out, `I${sec3 + 2}`, "事实类用中性提问口径；未知项与陷阱用保守口径（明确允许回答“无法确认”），仍给出实体才计幻觉。");

const sec4 = sec3Last + 2;
band(out, sec4, "4 真实用量合计", "J");
out.getRange(`A${sec4 + 1}:B${sec4 + 1}`).values = [["指标", "值"]];
head(out.getRange(`A${sec4 + 1}:B${sec4 + 1}`));
const totals = [
  ["真实模型调用次数", `=SUM(${totalRef("D")})`],
  ["prompt tokens", `=SUM(${totalRef("G")})`],
  ["completion tokens", `=SUM(${totalRef("H")})`],
  ["其中 reasoning tokens", `=SUM(${totalRef("I")})`],
  ["total tokens", `=SUM(${totalRef("J")})`],
  ["墙钟耗时 ms", rep.totals.wall_ms],
];
const totFirst = sec4 + 2;
const totLast = totFirst + totals.length - 1;
out.getRange(`A${totFirst}:A${totLast}`).values = totals.map((t) => [t[0]]);
for (let i = 0; i < totals.length; i++) {
  out.getRange(`B${totFirst + i}`).formulas = [[totals[i][1]]];
  out.getRange(`B${totFirst + i}`).format.numberFormat = "#,##0";
}
body(out.getRange(`A${totFirst}:B${totLast}`));
out.getRange(`B${totFirst}:B${totLast}`).format.font = { name: FONT, size: 10, bold: true };
note(out, `D${sec4 + 1}`, "前五行由「探针明细」台账汇总；探针问答按阶段合计登记一行，未逐条留痕。");

out.getRange("A:A").format.columnWidth = 22;
out.getRange("B:B").format.columnWidth = 20;
out.getRange("C:C").format.columnWidth = 16;
out.getRange("D:G").format.columnWidth = 16;
out.getRange("H:H").format.columnWidth = 14;
out.getRange("I:J").format.columnWidth = 18;

// ==========================================================================
// 回答原文
// ==========================================================================
tableTitle(raw, 1, "端到端探针原文（同模型、同问题，完整历史 vs 压缩后上下文）");
raw.getRange("A2:F2").values = [["案例", "探针", "类型", "问题", "上下文臂", "模型回答"]];
head(raw.getRange("A2:F2"));
const answers = [];
for (const c of cases) {
  for (const p of probeOf(c)) {
    answers.push([c.name, p.id, p.type, p.question, "完整历史", p.full_answer || ""]);
    answers.push([c.name, p.id, p.type, p.question, "压缩后上下文", p.compacted_answer || ""]);
  }
}
const ansFirst = 3;
const ansLast = ansFirst + answers.length - 1;
raw.getRange(`A${ansFirst}:F${ansLast}`).values = answers;
body(raw.getRange(`A${ansFirst}:F${ansLast}`));
raw.getRange(`A${ansFirst}:D${ansLast}`).format.verticalAlignment = "top";
raw.getRange(`E${ansFirst}:E${ansLast}`).format.verticalAlignment = "top";
raw.getRange(`F${ansFirst}:F${ansLast}`).format.verticalAlignment = "top";
raw.getRange(`D${ansFirst}:D${ansLast}`).format.wrapText = true;
raw.getRange(`F${ansFirst}:F${ansLast}`).format.wrapText = true;
for (let i = 0; i < answers.length; i++) {
  const text = answers[i][5];
  const breaks = (text.match(/\n/g) || []).length;
  const lines = Math.ceil(text.length / 60) + breaks;
  raw.getRange(`A${ansFirst + i}`).format.rowHeightPx = Math.min(420, Math.max(30, lines * 15 + 8));
}
raw.getRange("A:A").format.columnWidth = 12;
raw.getRange("B:B").format.columnWidth = 16;
raw.getRange("C:C").format.columnWidth = 10;
raw.getRange("D:D").format.columnWidth = 34;
raw.getRange("E:E").format.columnWidth = 14;
raw.getRange("F:F").format.columnWidth = 100;

// ==========================================================================
// 审计员结论
// ==========================================================================
tableTitle(judge, 1, "审计员逐条结论（矛盾 / 编造 / 遗漏）");
judge.getRange("A2:D2").values = [["案例", "类别", "摘要中的句子", "审计员理由 / 说明"]];
head(judge.getRange("A2:D2"));
const judgeRows = [];
for (const c of cases) {
  for (const it of c.judge.contradictions) judgeRows.push([c.name, "矛盾", it.quote || "", it.why || ""]);
  for (const it of c.judge.fabrications) judgeRows.push([c.name, "编造", it.quote || "", it.why || ""]);
  for (const it of c.judge.omissions) judgeRows.push([c.name, "遗漏", "", it]);
}
if (judgeRows.length) {
  const jFirst = 3;
  const jLast = jFirst + judgeRows.length - 1;
  judge.getRange(`A${jFirst}:D${jLast}`).values = judgeRows;
  body(judge.getRange(`A${jFirst}:D${jLast}`));
  judge.getRange(`A${jFirst}:A${jLast}`).format.verticalAlignment = "top";
  judge.getRange(`B${jFirst}:B${jLast}`).format.verticalAlignment = "top";
  judge.getRange(`C${jFirst}:D${jLast}`).format.wrapText = true;
  judge.getRange(`C${jFirst}:D${jLast}`).format.verticalAlignment = "top";
}
judge.getRange("A:A").format.columnWidth = 12;
judge.getRange("B:B").format.columnWidth = 8;
judge.getRange("C:C").format.columnWidth = 70;
judge.getRange("D:D").format.columnWidth = 70;

workbook.recalculate();
await fs.mkdir(outPath.replace(/[\\/][^\\/]+$/, ""), { recursive: true });
const file = await SpreadsheetFile.exportXlsx(workbook);
await file.save(outPath);
console.log(`wrote ${outPath}`);
