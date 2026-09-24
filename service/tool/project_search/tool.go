package project_search

import (
	"bufio"
	"context"
	"edu.agent.code/config"
	"edu.agent.code/utils/pathutil"
	"fmt"
	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	defaultMaxResults = 40
	maxAllowedResults = 80
	maxLineRunes      = 300
	maxFileBytes      = 512 * 1024 // 512kb
)

var searchableExtensions = map[string]bool{
	".go": true, ".mod": true, ".sum": true,
	".js": true, ".jsx": true, ".ts": true, ".tsx": true, ".mjs": true, ".cjs": true, ".mts": true, ".cts": true,
	".vue": true, ".svelte": true, ".astro": true,
	".css": true, ".scss": true, ".sass": true, ".less": true,
	".html": true, ".htm": true,
	".py": true, ".pyw": true, ".pyi": true,
	".java": true, ".kt": true, ".kts": true, ".scala": true, ".sc": true,
	".c": true, ".h": true, ".cpp": true, ".cc": true, ".cxx": true, ".hpp": true, ".hh": true, ".hxx": true,
	".cs": true, ".fs": true, ".fsx": true, ".vb": true,
	".rs": true, ".swift": true, ".dart": true, ".zig": true, ".nim": true,
	".m": true, ".mm": true,
	".php": true, ".rb": true, ".lua": true, ".pl": true, ".pm": true,
	".r": true, ".jl": true,
	".ex": true, ".exs": true, ".erl": true, ".hrl": true,
	".clj": true, ".cljs": true, ".cljc": true,
	".hs": true, ".lhs": true, ".ml": true, ".mli": true,
	".elm": true, ".purs": true, ".rkt": true, ".lisp": true, ".cl": true, ".el": true,
	".groovy": true, ".gradle": true,
	".sh": true, ".bash": true, ".zsh": true, ".fish": true, ".ps1": true, ".bat": true, ".cmd": true,
	".sql": true, ".graphql": true, ".gql": true, ".proto": true,
	".yaml": true, ".yml": true, ".json": true, ".toml": true, ".xml": true,
	".ini": true, ".cfg": true, ".conf": true, ".properties": true,
	".tf": true, ".tfvars": true, ".hcl": true,
	".cmake": true, ".mk": true, ".make": true,
	".md": true, ".mdx": true, ".txt": true, ".csv": true, ".tsv": true,
	".s": true, ".asm": true, ".sol": true, ".move": true,
}

type Input struct {
	Root       string `json:"root" jsonschema:"required,description=项目根目录绝对路径字符串，例如 D:/course/agent/edu.agent.course"` // 搜索范围根目录，必须存在。
	Query      string `json:"query" jsonschema:"required,description=要搜索的文件名、函数名、配置键或文本片段"`                            // 文件名、函数名、配置键或任意文本片段。
	MaxResults int    `json:"max_results,omitempty" jsonschema:"description=最大返回条数，默认 40，最大 80"`                       // 最大返回条数，过大会被限制到 maxAllowedResults。
}

func NewTool() (tool.BaseTool, error) {
	return toolutils.InferTool(
		"project_search",
		"只读搜索项目内文本和文件名，适合在项目问答中定位函数、路由、配置项和相关文件。不会执行命令，不会修改文件。",
		func(ctx context.Context, input Input) (string, error) {
			return doProjectSearch(ctx, input)
		},
	)
}

func doProjectSearch(ctx context.Context, input Input) (string, error) {
	root, err := pathutil.NormalizeExistingRoot(input.Root)
	if err != nil {
		return err.Error(), nil
	}
	workspace := config.GetLatestConfig().WorkSpace
	if err := pathutil.EnsureAllowedByRoot(root, workspace.Root, workspace.EnableEscaped); err != nil {
		return err.Error(), nil
	}

	query := strings.TrimSpace(input.Query)
	if query == "" {
		return fmt.Sprintf("query cannot be empty"), nil
	}
	limit := input.MaxResults
	if limit <= 0 {
		limit = defaultMaxResults
	}
	if limit > maxAllowedResults {
		limit = maxAllowedResults
	}

	matches, truncated, err := collectMatches(ctx, root, query, limit)
	if err != nil {
		return err.Error(), nil
	}
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "root=%s query=%s results=%d truncated=%t\n\n", root, query, len(matches), truncated)
	b.WriteString("--- matches ---\n")
	if len(matches) == 0 {
		b.WriteString("(no matches)\n")
		return b.String(), nil
	}
	for _, match := range matches {
		b.WriteString(match)
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String()), nil
}

func collectMatches(ctx context.Context, root, query string, limit int) ([]string, bool, error) {
	var (
		lowerQuery = strings.ToLower(query)
		matches    []string
		truncated  bool
	)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if path == root {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if len(matches) >= limit {
			truncated = true
			// 此处和PPT不同
			return filepath.SkipAll
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relSlash := filepath.ToSlash(rel)
		if strings.Contains(strings.ToLower(relSlash), lowerQuery) {
			matches = append(matches, fmt.Sprintf("%s: filename match", relSlash))
			if len(matches) >= limit {
				truncated = true
				// 此处和PPT不同
				return filepath.SkipAll
			}
		}
		if !isSearchableFile(path, d) {
			return nil
		}
		lineMatches, err := searchFile(path, relSlash, lowerQuery, limit-len(matches))
		if err != nil {
			return err
		}
		matches = append(matches, lineMatches...)
		if len(matches) >= limit {
			truncated = true
			// 此处和PPT不同
			return filepath.SkipAll
		}
		return nil
	})
	return matches, truncated, err
}

func isSearchableFile(path string, d fs.DirEntry) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if !searchableExtensions[ext] && ext != "" {
		return false
	}
	info, err := d.Info()
	if err != nil {
		return false
	}
	return info.Size() <= maxFileBytes
}

func searchFile(path, relSlash, lowerQuery string, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var matches []string
	reader := bufio.NewReader(file)
	lineNo := 0
	for {
		line, err := reader.ReadString('\n')
		lineNo++
		if len(line) > 0 {
			if !utf8.ValidString(line) {
				return matches, nil
			}
			if strings.Contains(strings.ToLower(line), lowerQuery) {
				matches = append(matches,
					fmt.Sprintf("%s: %d: %s", relSlash, lineNo, truncateLine(line)))
				if len(matches) >= limit {
					return matches, nil
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return matches, nil
		}
	}
	return matches, nil
}

func truncateLine(line string) string {
	line = strings.TrimSpace(line)
	runes := []rune(line)
	if len(runes) <= maxLineRunes {
		return line
	}
	return string(runes[:maxLineRunes]) + "...<truncated>"
}
