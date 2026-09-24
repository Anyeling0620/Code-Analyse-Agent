package read_files

import (
	"context"
	"edu.agent.code/utils/logger"
	"edu.agent.code/utils/pathutil"
	"fmt"
	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/gogf/gf/v2/util/gconv"
	"os"
	"strings"
)

const (
	maxReadFilesPerCall = 10
	maxReadExceptRunes  = 5000
)

type ReadFiles struct {
	Root      string   `json:"root" jsonschema:"required,description=项目根目录绝对路径字符串；示例：D:/workers/project；禁止传布尔值"`
	Files     []string `json:"files" jsonschema:"required,description=要读取的相对文件路径字符串数组（相对于项目根目录），通常来自目录清单、搜索结果或已读取文件中的引用；示例：[route/route.go]；禁止传布尔值或单个字符串"`
	StartLine int      `json:"start_line,omitempty" jsonschema:"description=可选起始行号，按 1 开始计数；读取大文件后半段或续读 truncated 内容时使用；省略或传 0 表示从第 1 行开始；禁止传字符串或布尔值"`
	EndLine   int      `json:"end_line,omitempty" jsonschema:"description=可选结束行号，包含该行；必须大于等于 start_line；省略或传 0 表示读到文件末尾；禁止传字符串或布尔值"`
}

func NewTool() (tool.BaseTool, error) {
	return toolutils.InferTool(
		"read_files",
		"批量读取指定目录的文本文件，并会进行脱敏读取操作。当需要读取源码、配置或文档正文时，优先使用该工具。"+
			"支持可选 start_line/end_line 按行范围读取；当返回 truncated=true 或需要文件后半段时，使用下一段行号继续读取，不要改用终端绕过。"+
			"读取后内容供模型分析，不要原样展示给用户。"+
			"返回格式：每个文件用 `--- file: <relpath> [size_bytes=X total_lines=Y line_range=A-B truncated=true|false next_start_line=Z] ---` 分隔，末尾给出 `--- N/M files ---` 计数。",
		func(ctx context.Context, input ReadFiles) (string, error) {
			return doReadFiles(ctx, input)
		},
	)
}

func doReadFiles(ctx context.Context, input ReadFiles) (string, error) {
	logger.Debug("doReadFiles", gconv.String(input))
	root, err := pathutil.NormalizeExistingRoot(input.Root)
	if err != nil {
		return "", fmt.Errorf("invalid read_files argument: root 必须是项目根路径, files必须是相对路径字符串数组"+
			"正确示例: {\"root\": \"D:/workers/project\", \"files\": [\"route/route.go\"]};"+
			"错误示例 : root=true, files=false;"+
			"本次错误原因：%w", err)
	}
	if len(input.Files) == 0 {
		return "", fmt.Errorf("invailid read_files arguments: files 不能为空, 必须指定相对路径的文件")
	}
	if input.StartLine < 0 || input.EndLine < 0 || (input.StartLine > 0 && input.EndLine > 0 && input.StartLine > input.EndLine) {
		return "", fmt.Errorf("invalid read_files argumentts start_line/end_line 必须非负，且start_line<end_line")
	}
	requested := len(input.Files)
	targets := input.Files
	if len(targets) > maxReadFilesPerCall {
		targets = targets[:maxReadExceptRunes]
	}
	var b strings.Builder
	readCount := 0
	for i, rel := range targets {
		select {
		case <-ctx.Done():
			return b.String(), ctx.Err()
		default:

		}
		if i > 0 {
			b.WriteString("\n")
		}
		meta, content, err := readFileFull(root, rel, input.StartLine, input.EndLine)
		if err != nil {
			_, _ = fmt.Fprintf(&b, "--- file:%s [error]---\n%v\n", rel, err)
			continue
		}
		_, _ = fmt.Fprintf(&b, "--- file:%s [%s]---\n%v\n", rel, meta, content)
		readCount++
	}
	b.WriteString("\n")
	if requested > maxReadFilesPerCall {
		_, _ = fmt.Fprintf(&b, "--- %d/%d files over_limit=%d ---\n", readCount, requested, maxReadFilesPerCall)
	} else {
		_, _ = fmt.Fprintf(&b, "--- %d/%d files ---\n", readCount, requested)
	}

	return b.String(), nil
}

func readFileFull(root, rel string, startLine, endLine int) (string, string, error) {
	path, err := pathutil.SafeJoinUnderRoot(root, rel)
	if err != nil {
		return "", "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	text := string(data) // TODO: 密钥等脱敏
	lines := splitLines(text)
	totalLines := len(lines)
	var selectedLines []string
	from, to := normalizeLineRange(startLine, endLine, totalLines)
	if from <= to {
		selectedLines = lines[from-1 : to]
	}
	selected, truncated, nextStartLine := joinLinesByRuneLimit(selectedLines, from)
	selected = strings.TrimSpace(selected)
	meta := fmt.Sprintf("size_bytes=%d total_lines=%d line_range=%d-%d truncated=%t\n",
		len(data), totalLines, from, to, truncated)
	if truncated && nextStartLine < totalLines {
		meta += fmt.Sprintf("next_start_line=%d", nextStartLine)
	}
	return meta, selected, nil
}

func joinLinesByRuneLimit(lines []string, startLine int) (string, bool, int) {
	var b strings.Builder
	for i, line := range lines {
		addition := len([]rune(line))
		if i > 0 {
			addition = addition + 1
		}
		if b.Len() > 0 && len(b.String())+addition > maxReadExceptRunes {
			return b.String() + "\n...<truncated>", true, startLine + i
		}
		if i > 0 {
			b.WriteString("\n")
		}
		lineRunes := []rune(line)
		if len(lineRunes) > maxReadExceptRunes {
			return string(lineRunes[:maxReadExceptRunes]) + "\n...<truncated>", true, startLine + i
		}
		b.WriteString(line)
	}
	return b.String(), false, startLine + len(lines)
}

func normalizeLineRange(startLine, endLine, totalLine int) (int, int) {
	if startLine < 0 {
		startLine = 1
	}
	if endLine <= 0 || endLine > totalLine {
		endLine = totalLine
	}
	return startLine, endLine
}

func splitLines(text string) []string {
	if len(text) == 0 {
		return nil
	}
	return strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
}
