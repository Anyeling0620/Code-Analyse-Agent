// 由 eval/compact/results.json 生成配套表单 context_compaction_real_model_eval.xlsx。
//
// 运行（node_modules 需指向 Codex 运行时依赖目录，ESM 按文件位置解析包）：
//   node <workdir>/build_xlsx.mjs \
//        --results <worktree>/eval/compact/results.json \
//        --out <worktree>/eval/compact/context_compaction_real_model_eval.xlsx
//
// 表单结构：
//   评估结果（输出）：压缩成效、关键事实保留率、触发阈值行为、真实延迟分布、
//                     端到端问答保真、真实用量合计
//   调用明细（来源）：真实模型调用台账、关键事实保留网格、端到端问答明细
//   回答原文（证据）：每问完整历史 / 压缩后上下文两臂的原始回答
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

const resultsPath = arg("results", "eval/compact/results.json");
const outPath = arg("out", "eval/compact/context_compaction_real_model_eval.xlsx");
const rep = JSON.parse(await fs.readFile(resultsPath, "utf8"));

const tf = (v) => (v ? "TRUE" : "FALSE");
// 生成时间取自 results.json，避免写死与真实跑批不一致。
const fmtStamp = (iso) =>
  String(iso).replace("T", " ").replace(/([+-]\d{2}):(\d{2})$/, " (UTC$1:$2)");
const factOrder = ["path", "symbol", "fuse", "collection", "dim"];
const factMeta = (id) => rep.qa.items.find((it) => it.fact === id) || {};

// ------------------------------------------------------------ 汇总表数据
const scaleRows = rep.scale_cases.map((c) => [
  c.name, c.rounds, c.runes_per_tool_result, c.trigger_tokens,
  c.before.messages, c.after.messages,
  c.before.estimated_tokens, c.after.estimated_tokens, "",
  c.usage.prompt_tokens, c.usage.completion_tokens, c.latency_ms,
]);

const thresholdRows = rep.threshold_cases.map((c) => [
  c.name, c.trigger_tokens, tf(c.triggered), c.model_calls.length,
  c.before.estimated_tokens, c.after.estimated_tokens,
]);

const latencyRows = rep.latency_repeats.map((c) => [
  c.name, c.latency_ms, c.usage.prompt_tokens, c.usage.completion_tokens,
  c.usage.reasoning_tokens, c.usage.total_tokens,
]);

const qaRows = factOrder.map((id) => {
  const it = factMeta(id);
  return [id, it.kind || "", it.keyword || "", tf(it.full_correct), tf(it.compacted_correct)];
});

// ------------------------------------------------------------ 明细表数据
const ledger = [];
let seq = 0;
const pushCall = (stage, caseName, call, note) => {
  ledger.push([
    ++seq, stage, caseName, 1, call.prompt_messages, call.prompt_estimated_tokens,
    call.prompt_tokens, call.completion_tokens, call.reasoning_tokens,
    call.total_tokens, call.latency_ms, call.finish_reason, note,
  ]);
};
for (const c of rep.scale_cases) pushCall("压缩成效", c.name, c.model_calls[0], "压缩摘要调用");
for (const c of rep.threshold_cases) {
  if (c.model_calls.length) pushCall("触发阈值", c.name, c.model_calls[0], "压缩摘要调用");
}
for (const c of rep.latency_repeats) pushCall("延迟重复", c.name, c.model_calls[0], "压缩摘要调用");

// 该阶段未逐条留痕，按阶段合计登记一行；reasoning 用总量残差推算。
const caseReasoning = ledger.reduce((s, r) => s + r[8], 0);
ledger.push([
  ++seq, "端到端问答", `${rep.qa.case}（5 问 × 2 臂）`, rep.qa.model_calls, "", "",
  rep.qa.prompt_tokens, rep.qa.completion_tokens, rep.totals.reasoning_tokens - caseReasoning,
  rep.qa.prompt_tokens + rep.qa.completion_tokens, rep.qa.latency_ms, "",
  "含 1 次压缩 + 10 次问答；reasoning 由总量残差推算",
]);

const ledgerFirst = 3;
const ledgerLast = ledgerFirst + ledger.length - 1;
const totalRef = (col) => `'调用明细'!${col}${ledgerFirst}:${col}${ledgerLast}`;

const retentionRows = factOrder.map((id) => {
  const it = factMeta(id);
  return [id, it.kind || "", it.keyword || "", ...rep.scale_cases.map((c) => tf(c.fact_retention[id]))];
});
const gridTitle = ledgerLast + 2;
const retFirst = gridTitle + 2;
const retLast = retFirst + retentionRows.length - 1;
const retRef = `'调用明细'!$D$${retFirst}:$D$${retLast}`;

const answerRows = [];
for (const id of factOrder) {
  const it = factMeta(id);
  answerRows.push([id, it.kind || "", it.question || "", "完整历史", it.full_answer || ""]);
  answerRows.push([id, it.kind || "", it.question || "", "压缩后上下文", it.compacted_answer || ""]);
}

// ------------------------------------------------------------------ 工作簿
const workbook = Workbook.create();
const out = workbook.worksheets.add("评估结果");
const detail = workbook.worksheets.add("调用明细");
const raw = workbook.worksheets.add("回答原文");
out.showGridLines = false;
detail.showGridLines = false;
raw.showGridLines = false;
out.tabColor = BAND_FILL;

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
const colCount = (letter) => letter.charCodeAt(0) - 64;
const note = (cell, text) => {
  const r = out.getRange(cell);
  r.values = [[text]];
  r.format.font = { name: FONT, size: 9, italic: true, color: "#595959" };
  r.format.verticalAlignment = "top";
  r.format.wrapText = false;
};
const tableTitle = (sheet, row, text) => {
  const r = sheet.getRange(`A${row}`);
  r.values = [[text]];
  r.format.font = { name: FONT, size: 11, bold: true };
};

// ------------------------------------------------------------ 标题与元信息
out.getRange("A2").values = [["上下文压缩真实模型评估"]];
out.getRange("A2").format.font = { name: FONT, size: 14, bold: true };
out.getRange("A2").format.rowHeight = 22;
out.getRange("A3:H3").format.borders = { bottom: { style: "thin", color: RULE } };
out.getRange("A3").format.rowHeight = 6;
out.getRange("A4:B8").values = [
  ["工作树", rep.branch],
  ["评估对象", "service/agent/compress 上下文压缩中间件"],
  ["摘要模型", `${rep.model} @ ${rep.base_url}`],
  ["生成时间", fmtStamp(rep.generated_at)],
  ["token 口径", "估算 = runes/4+1（仅用于压缩前后对比）；真实 token 取自模型响应 usage"],
];
out.getRange("A4:B8").format.font = { name: FONT, size: 10 };
out.getRange("A4:B8").format.verticalAlignment = "center";
out.getRange("A4:A8").format.font = { name: FONT, size: 10, color: "#595959" };

// ------------------------------------------------------------- 1 压缩成效
band(out, 10, "1 压缩成效（真实模型摘要）", "L");
out.getRange("A11:L11").values = [[
  "案例", "轮次", "单条 runes", "触发阈值", "消息数前", "消息数后",
  "估算 token 前", "估算 token 后", "压缩比", "真实 prompt tokens",
  "真实 completion tokens", "延迟 ms",
]];
head(out.getRange("A11:L11"));
const scaleLast = 11 + scaleRows.length;
out.getRange(`A12:L${scaleLast}`).values = scaleRows;
body(out.getRange(`A12:L${scaleLast}`));
for (let i = 0; i < scaleRows.length; i++) {
  out.getRange(`I${12 + i}`).formulas = [[`=IF(G${12 + i}=0,0,1-H${12 + i}/G${12 + i})`]];
}
out.getRange(`E12:F${scaleLast}`).format.numberFormat = "#,##0";
out.getRange(`G12:H${scaleLast}`).format.numberFormat = "#,##0";
out.getRange(`I12:I${scaleLast}`).format.numberFormat = "0.0%";
out.getRange(`J12:K${scaleLast}`).format.numberFormat = "#,##0";
out.getRange(`L12:L${scaleLast}`).format.numberFormat = "#,##0";
note("N11", "压缩比按估算口径：1 − 压缩后 ÷ 压缩前。");
note("N12", "真实 token 是单次摘要调用实际上传 / 返回的 usage，含 system、全部历史与摘要指令。");

// ---------------------------------------------------- 2 关键事实保留率
const sec2 = scaleLast + 2;
band(out, sec2, "2 关键事实保留率（压缩摘要文本中是否仍含关键标识符）", "L");
out.getRange(`A${sec2 + 1}:C${sec2 + 1}`).values = [["案例", "保留率", "未保留的事实"]];
head(out.getRange(`A${sec2 + 1}:C${sec2 + 1}`));
const retRows = rep.scale_cases.map((c) => {
  const missing = factOrder.filter((id) => !c.fact_retention[id]).map((id) => factMeta(id).keyword || id);
  return [c.name, "", missing.length ? missing.join("、") : "-"];
});
out.getRange(`A${sec2 + 2}:C${sec2 + 1 + retRows.length}`).values = retRows;
body(out.getRange(`A${sec2 + 2}:C${sec2 + 1 + retRows.length}`));
for (let i = 0; i < retRows.length; i++) {
  out.getRange(`B${sec2 + 2 + i}`).formulas = [[`=COUNTIF(${retRef},"TRUE")/${retentionRows.length}`]];
}
out.getRange(`B${sec2 + 2}:B${sec2 + 1 + retRows.length}`).format.numberFormat = "0%";

// --------------------------------------------------- 3 触发阈值行为
const sec3 = sec2 + 2 + retRows.length + 1;
band(out, sec3, "3 触发阈值行为", "L");
out.getRange(`A${sec3 + 1}:F${sec3 + 1}`).values = [[
  "案例", "阈值", "是否触发", "摘要调用次数", "估算 token 前", "估算 token 后",
]];
head(out.getRange(`A${sec3 + 1}:F${sec3 + 1}`));
out.getRange(`A${sec3 + 2}:F${sec3 + 1 + thresholdRows.length}`).values = thresholdRows;
body(out.getRange(`A${sec3 + 2}:F${sec3 + 1 + thresholdRows.length}`));
out.getRange(`B${sec3 + 2}:B${sec3 + 1 + thresholdRows.length}`).format.numberFormat = "#,##0";
out.getRange(`D${sec3 + 2}:F${sec3 + 1 + thresholdRows.length}`).format.numberFormat = "#,##0";
out.getRange(`C${sec3 + 2}:C${sec3 + 1 + thresholdRows.length}`).format.horizontalAlignment = "center";
note(`H${sec3 + 1}`, "同一份 10 轮 × 8k 历史：阈值 2000 触发压缩，阈值 100000 完全不动。");

// --------------------------------------------------- 4 真实延迟分布
const sec4 = sec3 + 2 + thresholdRows.length + 1;
band(out, sec4, "4 真实延迟分布（同形状重复 3 次）", "L");
out.getRange(`A${sec4 + 1}:F${sec4 + 1}`).values = [[
  "运行", "延迟 ms", "prompt tokens", "completion tokens", "reasoning tokens", "total tokens",
]];
head(out.getRange(`A${sec4 + 1}:F${sec4 + 1}`));
const latFirst = sec4 + 2;
const latLast = sec4 + 1 + latencyRows.length;
out.getRange(`A${latFirst}:F${latLast}`).values = latencyRows;
body(out.getRange(`A${latFirst}:F${latLast}`));
out.getRange(`A${latLast + 1}:F${latLast + 1}`).values = [["平均", "", "", "", "", ""]];
body(out.getRange(`A${latLast + 1}:F${latLast + 1}`));
out.getRange(`A${latLast + 1}:F${latLast + 1}`).format.font = { name: FONT, size: 10, bold: true };
for (const col of ["B", "C", "D", "E", "F"]) {
  out.getRange(`${col}${latLast + 1}`).formulas = [[`=AVERAGE(${col}${latFirst}:${col}${latLast})`]];
}
out.getRange(`B${latFirst}:B${latLast + 1}`).format.numberFormat = "#,##0";
out.getRange(`C${latFirst}:F${latLast + 1}`).format.numberFormat = "#,##0";

// ---------------------------------------------- 5 端到端问答保真
const sec5 = latLast + 3;
band(out, sec5, `5 端到端问答保真（${rep.qa.case}：完整历史 vs 压缩后上下文）`, "L");
out.getRange(`A${sec5 + 1}:E${sec5 + 1}`).values = [["事实", "类型", "关键词", "完整历史答对", "压缩后答对"]];
head(out.getRange(`A${sec5 + 1}:E${sec5 + 1}`));
const qaFirst = sec5 + 2;
const qaLast = sec5 + 1 + qaRows.length;
out.getRange(`A${qaFirst}:E${qaLast}`).values = qaRows;
body(out.getRange(`A${qaFirst}:E${qaLast}`));
out.getRange(`D${qaFirst}:E${qaLast}`).format.horizontalAlignment = "center";
out.getRange(`A${qaLast + 1}:E${qaLast + 1}`).values = [["答对合计", "", "", "", ""]];
body(out.getRange(`A${qaLast + 1}:E${qaLast + 1}`));
out.getRange(`A${qaLast + 1}:E${qaLast + 1}`).format.font = { name: FONT, size: 10, bold: true };
for (const col of ["D", "E"]) {
  out.getRange(`${col}${qaLast + 1}`).formulas = [[
    `=COUNTIF(${col}${qaFirst}:${col}${qaLast},"TRUE")&" / "&COUNTA(${col}${qaFirst}:${col}${qaLast})`,
  ]];
}
note(`G${sec5 + 1}`, "判定口径：回答文本出现该关键词即算答对，不做语义评分；原始回答见「回答原文」。");
{
  const missed = rep.qa.items
    .filter((it) => !it.compacted_correct)
    .map((it) => `${it.fact}（${it.keyword}）`);
  note(
    `G${sec5 + 2}`,
    missed.length
      ? `本例压缩后漏掉 ${missed.join("、")}：摘要里仍含对应关键词，但该次回答未采纳。`
      : "本例压缩后 5 项关键事实全部答对，与完整历史臂持平；真实模型存在随机性。",
  );
}

// --------------------------------------------------- 6 真实用量合计
const sec6 = qaLast + 3;
band(out, sec6, "6 真实用量合计", "L");
out.getRange(`A${sec6 + 1}:B${sec6 + 1}`).values = [["指标", "值"]];
head(out.getRange(`A${sec6 + 1}:B${sec6 + 1}`));
const totals = [
  ["真实模型调用次数", `=SUM(${totalRef("D")})`],
  ["prompt tokens", `=SUM(${totalRef("G")})`],
  ["completion tokens", `=SUM(${totalRef("H")})`],
  ["其中 reasoning tokens", `=SUM(${totalRef("I")})`],
  ["total tokens", `=SUM(${totalRef("J")})`],
  ["墙钟耗时 ms", rep.totals.wall_ms],
];
out.getRange(`A${sec6 + 2}:A${sec6 + 1 + totals.length}`).values = totals.map((t) => [t[0]]);
for (let i = 0; i < totals.length; i++) {
  out.getRange(`B${sec6 + 2 + i}`).formulas = [[totals[i][1]]];
  out.getRange(`B${sec6 + 2 + i}`).format.numberFormat = "#,##0";
}
body(out.getRange(`A${sec6 + 2}:B${sec6 + 1 + totals.length}`));
out.getRange(`B${sec6 + 2}:B${sec6 + 1 + totals.length}`).format.font = { name: FONT, size: 10, bold: true };
note(`D${sec6 + 1}`, "前五行由「调用明细」台账汇总；墙钟耗时为整轮评估端到端时长，非各调用之和。");

out.getRange("A:A").format.columnWidth = 24;
out.getRange("B:B").format.columnWidth = 16;
out.getRange("C:C").format.columnWidth = 20;
out.getRange("D:D").format.columnWidth = 11;
out.getRange("E:F").format.columnWidth = 10;
out.getRange("G:H").format.columnWidth = 14;
out.getRange("I:I").format.columnWidth = 10;
out.getRange("J:K").format.columnWidth = 16;
out.getRange("L:L").format.columnWidth = 12;

// ------------------------------------------------------------- 调用明细
tableTitle(detail, 1, "真实模型调用台账");
detail.getRange("A2:M2").values = [[
  "序号", "阶段", "案例", "调用次数", "输入消息数", "估算输入 tokens",
  "真实 prompt tokens", "真实 completion tokens", "真实 reasoning tokens",
  "真实 total tokens", "延迟 ms", "完成原因", "备注",
]];
head(detail.getRange("A2:M2"));
detail.getRange(`A${ledgerFirst}:M${ledgerLast}`).values = ledger;
body(detail.getRange(`A${ledgerFirst}:M${ledgerLast}`));
detail.getRange(`D${ledgerFirst}:J${ledgerLast}`).format.numberFormat = "#,##0";
detail.getRange(`K${ledgerFirst}:K${ledgerLast}`).format.numberFormat = "#,##0";
detail.getRange(`A${ledgerFirst}:A${ledgerLast}`).format.horizontalAlignment = "center";
detail.getRange(`L${ledgerFirst}:L${ledgerLast}`).format.horizontalAlignment = "center";

tableTitle(detail, gridTitle, "关键事实在压缩摘要中的保留情况");
detail.getRange(`A${gridTitle + 1}:F${gridTitle + 1}`).values = [[
  "事实", "类型", "关键词", ...rep.scale_cases.map((c) => c.name),
]];
head(detail.getRange(`A${gridTitle + 1}:F${gridTitle + 1}`));
detail.getRange(`A${retFirst}:F${retLast}`).values = retentionRows;
body(detail.getRange(`A${retFirst}:F${retLast}`));

const qaTitle = retLast + 2;
tableTitle(detail, qaTitle, "端到端问答明细");
detail.getRange(`A${qaTitle + 1}:E${qaTitle + 1}`).values = [[
  "事实", "类型", "关键词", "完整历史答对", "压缩后答对",
]];
head(detail.getRange(`A${qaTitle + 1}:E${qaTitle + 1}`));
detail.getRange(`A${qaTitle + 2}:E${qaTitle + 1 + qaRows.length}`).values = qaRows;
body(detail.getRange(`A${qaTitle + 2}:E${qaTitle + 1 + qaRows.length}`));

detail.getRange("A:A").format.columnWidth = 12;
detail.getRange("B:B").format.columnWidth = 12;
detail.getRange("C:C").format.columnWidth = 26;
detail.getRange("D:J").format.columnWidth = 13;
detail.getRange("K:K").format.columnWidth = 10;
detail.getRange("L:L").format.columnWidth = 11;
detail.getRange("M:M").format.columnWidth = 32;

// ------------------------------------------------------------- 回答原文
tableTitle(raw, 1, "端到端问答原文（同一模型、同一问题，两臂上下文不同）");
raw.getRange("A2:E2").values = [["事实", "类型", "问题", "上下文臂", "模型回答"]];
head(raw.getRange("A2:E2"));
const ansFirst = 3;
const ansLast = ansFirst + answerRows.length - 1;
raw.getRange(`A${ansFirst}:E${ansLast}`).values = answerRows;
body(raw.getRange(`A${ansFirst}:E${ansLast}`));
raw.getRange(`A${ansFirst}:C${ansLast}`).format.verticalAlignment = "top";
raw.getRange(`D${ansFirst}:D${ansLast}`).format.verticalAlignment = "top";
raw.getRange(`E${ansFirst}:E${ansLast}`).format.verticalAlignment = "top";
raw.getRange(`C${ansFirst}:C${ansLast}`).format.wrapText = true;
raw.getRange(`E${ansFirst}:E${ansLast}`).format.wrapText = true;
for (let i = 0; i < answerRows.length; i++) {
  const text = answerRows[i][4];
  const breaks = (text.match(/\n/g) || []).length;
  const lines = Math.ceil(text.length / 44) + breaks;
  raw.getRange(`A${ansFirst + i}`).format.rowHeightPx =
    Math.min(340, Math.max(30, lines * 15 + 8));
}
raw.getRange("A:A").format.columnWidth = 12;
raw.getRange("B:B").format.columnWidth = 10;
raw.getRange("C:C").format.columnWidth = 34;
raw.getRange("D:D").format.columnWidth = 14;
raw.getRange("E:E").format.columnWidth = 96;

workbook.recalculate();
await fs.mkdir(outPath.replace(/[\\/][^\\/]+$/, ""), { recursive: true });
const file = await SpreadsheetFile.exportXlsx(workbook);
await file.save(outPath);
console.log(`wrote ${outPath}`);
