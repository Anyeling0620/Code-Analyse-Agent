package codeindex

import (
	"context"
	"fmt"
	"path"
	"strings"

	"edu.agent.code/utils/logger"

	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
)

const (
	toolNameOutline       = "code_outline"
	toolNameFindSymbol    = "code_find_symbol"
	toolNameFileDeps      = "code_file_deps"
	toolNameTypeRelations = "code_type_relations"
)

const (
	noProjectTips = "当前会话还没有确定项目，无法查询结构化索引。" +
		"请先让用户给出项目（git 地址或本机绝对路径），由 repo_fetch 准备好仓库后再重试；" +
		"本轮可以先用 project_scan / project_search / read_files 继续。"
	indexingTips      = "正在建立结构化索引，请稍后重试（可先用 project_search 继续）。"
	indexNotReadyTips = "当前项目的结构化索引尚未就绪（状态：%s），暂时无法精确查询符号。" +
		"可以先用 project_search / project_files 定位，或稍后重试。"
	notFoundTips    = "索引里没有这个路径：%s。请使用项目根目录起算的相对路径（例如 service/rag/project.go）。"
	symbolLimitTips = "结果已按上限截断，需要更精确的结果请补充 kind 或使用更长的符号名。"
)

// buildTools 构造 4 个查询工具。构造失败只记日志并跳过该工具：
// 单个工具的 schema 问题不应该让整个服务起不来。
func (ix *Indexer) buildTools() []tool.BaseTool {
	contextNote := "这是结构化索引查询（精确），不是语义检索；不知道符号名时用 rag_retriever / project_search。"

	outline, outlineErr := toolutils.InferTool(
		toolNameOutline,
		"查看某个文件或目录里声明了哪些符号（函数/方法/类型），返回 kind、签名和行号。"+
			"适合先摸清一个文件的结构再决定读哪一段。"+contextNote,
		ix.outlineTool,
	)
	findSymbol, findErr := toolutils.InferTool(
		toolNameFindSymbol,
		"按名字精确查找符号定义（函数/方法/类型/常量），返回 path:line、kind 与签名。"+
			"匹配顺序是等值→前缀→包含，符号名不完整时也能用；知道类型就能过滤掉同名方法。"+contextNote,
		ix.findSymbolTool,
	)
	fileDeps, depsErr := toolutils.InferTool(
		toolNameFileDeps,
		"查看一个文件的依赖关系：它 import 了谁（出边），以及谁 import 了它（入边）。"+
			"用于评估改动影响面、定位循环依赖；解析不到本地路径时会标注\"未解析\"。"+contextNote,
		ix.fileDepsTool,
	)
	typeRelations, relationsErr := toolutils.InferTool(
		toolNameTypeRelations,
		"查看一个类型的继承/实现/嵌入关系：它的父类型、实现的接口、嵌入的类型，"+
			"以及谁继承或实现了它。启发式推断出来的边会明确标注，不能当成事实。"+contextNote,
		ix.typeRelationsTool,
	)
	entries := []struct {
		name string
		tool tool.BaseTool
		err  error
	}{
		{toolNameOutline, outline, outlineErr},
		{toolNameFindSymbol, findSymbol, findErr},
		{toolNameFileDeps, fileDeps, depsErr},
		{toolNameTypeRelations, typeRelations, relationsErr},
	}
	built := make([]tool.BaseTool, 0, len(entries))
	for _, entry := range entries {
		if entry.err != nil {
			logger.Error("code index: 构造工具失败 name=%s err=%v", entry.name, entry.err)
			continue
		}
		if entry.tool != nil {
			built = append(built, entry.tool)
		}
	}
	return built
}

type outlineInput struct {
	Path string `json:"path" jsonschema:"required,description=项目根目录起算的相对路径，可以是文件或目录；留空表示项目根。"`
}

type findSymbolInput struct {
	Name  string `json:"name" jsonschema:"required,description=符号名，可以是完整名（NewSession）或不完整片段（Session）。"`
	Kind  string `json:"kind,omitempty" jsonschema:"description=可选，限定符号类型：func/method/struct/interface/class/type/enum/const/var/module。"`
	Limit int    `json:"limit,omitempty" jsonschema:"description=返回条数上限，默认使用服务端配置（通常 50）。"`
}

type fileDepsInput struct {
	Path string `json:"path" jsonschema:"required,description=项目根目录起算的文件相对路径，例如 service/rag/project.go。"`
}

type typeRelationsInput struct {
	Name string `json:"name" jsonschema:"required,description=类型名，例如 ProjectIndexer 或 BaseStore。"`
}

// projectForQuery 解析当前项目并检查索引状态；第三个返回值非空时就是给模型的提示，
// 此时调用方直接返回它，不再查库。
func (ix *Indexer) projectForQuery(ctx context.Context) (Ref, Status, string) {
	ref, ok := ix.resolveCurrent(ctx)
	if !ok || ref.ProjectID == "" {
		return Ref{}, Status{}, noProjectTips
	}
	status := ix.Status(ctx, ref.ProjectID)
	if status.Status == StatusReady {
		return ref, status, ""
	}
	if status.Status == StatusIndexing {
		return ref, status, indexingTips
	}
	// 未就绪但知道项目根：顺手触发一次后台重建，让下一次调用能命中。
	if ref.Root != "" {
		ix.EnsureIndexedAsync(ctx, ref)
		return ref, status, indexingTips
	}
	return ref, status, fmt.Sprintf(indexNotReadyTips, describeStatus(status))
}

func describeStatus(status Status) string {
	switch status.Status {
	case StatusNone:
		return "还没有建立"
	case StatusIndexing:
		return "正在构建"
	case StatusReady:
		return "已就绪"
	case StatusFailed:
		if status.LastError != "" {
			return "上次构建失败：" + status.LastError
		}
		return "构建失败"
	default:
		return string(status.Status)
	}
}

// truncationNote 把"索引不完整"如实告诉模型，避免它以为查不到就是不存在。
func truncationNote(status Status) string {
	if !status.Truncated {
		return ""
	}
	return "\n注意：该项目索引达到规模上限，内容不完整；未查到的符号可能尚未索引。"
}

// normalizeRel 把模型给的路径归一化成索引里的形式（斜杠分隔、无前导 ./），
// 并拒绝越界路径，避免拿 "../.." 读到项目外的文件。
func normalizeRel(raw string) (string, bool) {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	raw = strings.TrimPrefix(raw, "./")
	raw = strings.Trim(raw, "/")
	if raw == "" || raw == "." {
		return "", true
	}
	cleaned := path.Clean(raw)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", false
	}
	return cleaned, true
}

func (ix *Indexer) outlineTool(ctx context.Context, input outlineInput) (string, error) {
	ref, status, tips := ix.projectForQuery(ctx)
	if tips != "" {
		return tips, nil
	}
	rel, ok := normalizeRel(input.Path)
	if !ok {
		return "路径不能越出项目根目录，请使用项目内相对路径。", nil
	}
	if rel != "" {
		isFile, err := ix.store.hasFile(ctx, ref.ProjectID, rel)
		if err != nil {
			logger.Error("code index: 查询文件失败 path=%s err=%v", rel, err)
			return "读取索引失败，请稍后重试。", nil
		}
		if isFile {
			return ix.renderFileOutline(ctx, ref.ProjectID, rel)
		}
	}
	return ix.renderDirOutline(ctx, ref.ProjectID, rel, status)
}

func (ix *Indexer) renderFileOutline(ctx context.Context, projectID, rel string) (string, error) {
	symbols, err := ix.store.fileSymbols(ctx, projectID, rel)
	if err != nil {
		logger.Error("code index: 查询符号失败 path=%s err=%v", rel, err)
		return "读取索引失败，请稍后重试。", nil
	}
	sortSymbols(symbols)
	var builder strings.Builder
	builder.WriteString("文件 ")
	builder.WriteString(rel)
	builder.WriteString(fmt.Sprintf("：%d 个符号\n", len(symbols)))
	if len(symbols) == 0 {
		builder.WriteString("该文件里没有识别到函数/类型声明（可能是纯配置或数据文件）。\n")
		return builder.String(), nil
	}
	limit := ix.conf.MaxResults
	for index, symbol := range symbols {
		if index >= limit {
			builder.WriteString(fmt.Sprintf("…另有 %d 个符号未列出（已达返回上限 %d）\n", len(symbols)-limit, limit))
			break
		}
		builder.WriteString(formatSymbolLine(symbol, "  "))
	}
	return builder.String(), nil
}

func (ix *Indexer) renderDirOutline(ctx context.Context, projectID, dir string, status Status) (string, error) {
	total, err := ix.store.countFilesUnder(ctx, projectID, dir)
	if err != nil {
		logger.Error("code index: 统计目录文件失败 dir=%s err=%v", dir, err)
		return "读取索引失败，请稍后重试。", nil
	}
	if total == 0 {
		return fmt.Sprintf(notFoundTips, displayPath(dir)), nil
	}
	limit := ix.conf.MaxResults
	files, err := ix.store.filesUnder(ctx, projectID, dir, limit)
	if err != nil {
		logger.Error("code index: 列目录文件失败 dir=%s err=%v", dir, err)
		return "读取索引失败，请稍后重试。", nil
	}
	symbolsByPath, err := ix.store.symbolsForPaths(ctx, projectID, filePaths(files))
	if err != nil {
		logger.Error("code index: 批量查询符号失败 dir=%s err=%v", dir, err)
		return "读取索引失败，请稍后重试。", nil
	}
	var builder strings.Builder
	builder.WriteString("目录 ")
	builder.WriteString(displayPath(dir))
	builder.WriteString(fmt.Sprintf("：共 %d 个已索引文件，下面列出 %d 个\n", total, len(files)))
	for _, file := range files {
		symbols := symbolsByPath[file.Path]
		sortSymbols(symbols)
		builder.WriteString(fmt.Sprintf("\n%s（%s，%d 行，%d 个符号）\n", file.Path, file.Language, file.LineCount, file.SymbolCount))
		shown := 0
		for _, symbol := range symbols {
			// 目录概览只看顶层声明，方法/字段由 code_outline(单个文件) 提供，
			// 否则一个类就能把整页占满。
			if symbol.Parent != "" {
				continue
			}
			if shown >= 3 {
				break
			}
			builder.WriteString(formatSymbolLine(symbol, "  "))
			shown++
		}
		if len(symbols) > shown {
			builder.WriteString(fmt.Sprintf("  …共 %d 个符号\n", len(symbols)))
		}
	}
	if int64(len(files)) < total {
		builder.WriteString(fmt.Sprintf("\n…另有 %d 个文件未列出（已达返回上限 %d）\n", total-int64(len(files)), limit))
	}
	builder.WriteString(truncationNote(status))
	return builder.String(), nil
}

func (ix *Indexer) findSymbolTool(ctx context.Context, input findSymbolInput) (string, error) {
	ref, status, tips := ix.projectForQuery(ctx)
	if tips != "" {
		return tips, nil
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return "符号名不能为空。请提供函数/类型名，例如 FindSymbol。", nil
	}
	limit := input.Limit
	if limit <= 0 || limit > ix.conf.MaxResults {
		limit = ix.conf.MaxResults
	}
	rows, err := ix.store.findSymbols(ctx, ref.ProjectID, name, strings.TrimSpace(input.Kind), limit)
	if err != nil {
		logger.Error("code index: 查找符号失败 name=%s err=%v", name, err)
		return "查询符号失败，请稍后重试。", nil
	}
	if len(rows) == 0 {
		message := fmt.Sprintf("没有找到符号 %q（索引内共 %d 条符号）。可以换更短的片段重试，或用 rag_retriever / project_search 做模糊定位。",
			name, status.SymbolCount)
		return message + truncationNote(status), nil
	}
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("符号 %q 的匹配（共 %d 条，按 等值→前缀→包含 排序）：\n", name, len(rows)))
	for _, row := range rows {
		builder.WriteString(fmt.Sprintf("- %s:%d  [%s]  %s%s\n",
			row.Path, row.LineStart, kindLabel(row.Kind), signatureText(row.Signature), parentText(row.Parent)))
	}
	if len(rows) >= limit {
		builder.WriteString(symbolLimitTips + "\n")
	}
	builder.WriteString(truncationNote(status))
	return builder.String(), nil
}

func (ix *Indexer) fileDepsTool(ctx context.Context, input fileDepsInput) (string, error) {
	ref, status, tips := ix.projectForQuery(ctx)
	if tips != "" {
		return tips, nil
	}
	rel, ok := normalizeRel(input.Path)
	if !ok || rel == "" {
		return "请提供项目内的文件相对路径，例如 service/rag/project.go。", nil
	}
	isFile, err := ix.store.hasFile(ctx, ref.ProjectID, rel)
	if err != nil {
		logger.Error("code index: 查询文件失败 path=%s err=%v", rel, err)
		return "读取索引失败，请稍后重试。", nil
	}
	if !isFile {
		return fmt.Sprintf(notFoundTips, rel), nil
	}
	outgoing, err := ix.store.importsFrom(ctx, ref.ProjectID, rel)
	if err != nil {
		logger.Error("code index: 查询出边失败 path=%s err=%v", rel, err)
		return "读取索引失败，请稍后重试。", nil
	}
	incoming, err := ix.store.importsInto(ctx, ref.ProjectID, rel)
	if err != nil {
		logger.Error("code index: 查询入边失败 path=%s err=%v", rel, err)
		return "读取索引失败，请稍后重试。", nil
	}
	limit := ix.conf.MaxResults
	var builder strings.Builder
	builder.WriteString("文件 ")
	builder.WriteString(rel)
	builder.WriteString("\n\n出边（本文件 import 了谁）：\n")
	if len(outgoing) == 0 {
		builder.WriteString("- 没有记录到 import。\n")
	}
	for index, edge := range outgoing {
		if index >= limit {
			builder.WriteString(fmt.Sprintf("- …另有 %d 条未列出\n", len(outgoing)-limit))
			break
		}
		builder.WriteString(fmt.Sprintf("- L%d  %s → %s\n", edge.Line, edge.ToRaw, resolvedPathText(edge.ToPath)))
	}
	builder.WriteString("\n入边（谁 import 了本文件）：\n")
	if len(incoming) == 0 {
		builder.WriteString("- 没有查到引用它的文件。\n")
	}
	for index, edge := range incoming {
		if index >= limit {
			builder.WriteString(fmt.Sprintf("- …另有 %d 条未列出\n", len(incoming)-limit))
			break
		}
		builder.WriteString(fmt.Sprintf("- %s:L%d  import %s\n", edge.FromPath, edge.Line, edge.ToRaw))
	}
	builder.WriteString(truncationNote(status))
	return builder.String(), nil
}

func (ix *Indexer) typeRelationsTool(ctx context.Context, input typeRelationsInput) (string, error) {
	ref, status, tips := ix.projectForQuery(ctx)
	if tips != "" {
		return tips, nil
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return "类型名不能为空，例如 ProjectIndexer。", nil
	}
	outgoing, err := ix.store.typeEdgesFrom(ctx, ref.ProjectID, name)
	if err != nil {
		logger.Error("code index: 查询类型出边失败 name=%s err=%v", name, err)
		return "读取索引失败，请稍后重试。", nil
	}
	incoming, err := ix.store.typeEdgesInto(ctx, ref.ProjectID, name)
	if err != nil {
		logger.Error("code index: 查询类型入边失败 name=%s err=%v", name, err)
		return "读取索引失败，请稍后重试。", nil
	}
	var builder strings.Builder
	builder.WriteString("类型 ")
	builder.WriteString(name)
	builder.WriteString(" 的关系（来自结构化索引；标注\"启发式\"的边是正则推断，需读源码确认）\n")
	limit := ix.conf.MaxResults

	builder.WriteString("\n出边（它继承/实现/嵌入了谁）：\n")
	if len(outgoing) == 0 {
		builder.WriteString("- 没有记录到父类型/接口/嵌入关系。\n")
	}
	for index, edge := range outgoing {
		if index >= limit {
			builder.WriteString(fmt.Sprintf("- …另有 %d 条未列出\n", len(outgoing)-limit))
			break
		}
		builder.WriteString(fmt.Sprintf("- %s %s%s（%s:%d）\n",
			edgeKindLabel(edge.Kind), edge.ToRaw, confidenceMark(edge.Confidence), edge.FromPath, edge.Line))
	}

	builder.WriteString("\n入边（谁继承/实现了它）：\n")
	if len(incoming) == 0 {
		builder.WriteString("- 没有查到继承或实现它的类型。\n")
	}
	for index, edge := range incoming {
		if index >= limit {
			builder.WriteString(fmt.Sprintf("- …另有 %d 条未列出\n", len(incoming)-limit))
			break
		}
		subject := firstNonEmpty(edge.FromSymbol, edge.FromPath)
		builder.WriteString(fmt.Sprintf("- %s %s%s（%s:%d）\n",
			subject, edgeKindLabel(edge.Kind), confidenceMark(edge.Confidence), edge.FromPath, edge.Line))
	}
	builder.WriteString(truncationNote(status))
	return builder.String(), nil
}

func filePaths(files []fileRow) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	return paths
}

func displayPath(rel string) string {
	if rel == "" {
		return "（项目根）"
	}
	return rel
}

func formatSymbolLine(symbol symbolRow, indent string) string {
	lineRange := fmt.Sprintf("L%d", symbol.LineStart)
	if symbol.LineEnd > symbol.LineStart {
		lineRange = fmt.Sprintf("L%d-L%d", symbol.LineStart, symbol.LineEnd)
	}
	return fmt.Sprintf("%s%s  %-9s %s%s\n", indent, lineRange, kindLabel(symbol.Kind),
		signatureOrName(symbol), parentText(symbol.Parent))
}

func signatureOrName(symbol symbolRow) string {
	if strings.TrimSpace(symbol.Signature) != "" {
		return signatureText(symbol.Signature)
	}
	return symbol.Name
}

func signatureText(signature string) string {
	signature = strings.TrimSpace(strings.ReplaceAll(signature, "\n", " "))
	runes := []rune(signature)
	if len(runes) > 160 {
		return string(runes[:160]) + "…"
	}
	return signature
}

func parentText(parent string) string {
	if strings.TrimSpace(parent) == "" {
		return ""
	}
	return "  (属于 " + parent + ")"
}

func resolvedPathText(toPath string) string {
	if strings.TrimSpace(toPath) == "" {
		return "（未解析到项目内文件）"
	}
	if strings.HasSuffix(toPath, "/") {
		return toPath + "（目录下全部文件）"
	}
	return toPath
}

func confidenceMark(confidence string) string {
	if confidence == ConfidenceHeuristic {
		return "  [启发式]"
	}
	return ""
}

func kindLabel(kind string) string {
	switch kind {
	case KindFunc:
		return "func"
	case KindMethod:
		return "method"
	case KindStruct:
		return "struct"
	case KindInterface:
		return "interface"
	case KindClass:
		return "class"
	case KindTrait:
		return "trait"
	case KindType:
		return "type"
	case KindEnum:
		return "enum"
	case KindConst:
		return "const"
	case KindVar:
		return "var"
	case KindModule:
		return "module"
	default:
		if kind == "" {
			return "symbol"
		}
		return kind
	}
}

func edgeKindLabel(kind string) string {
	switch kind {
	case EdgeExtends:
		return "继承/扩展"
	case EdgeImplements:
		return "实现"
	case EdgeEmbeds:
		return "嵌入"
	case EdgeImport:
		return "import"
	default:
		return kind
	}
}
