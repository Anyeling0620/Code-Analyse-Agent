package project_scan

import (
	"context"
	"edu.agent.code/config"
	"edu.agent.code/utils/pathutil"
	"fmt"
	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	defaultMaxDepth       = 4
	maxAllowedDepth       = 8
	maxProjectScanEntries = 400
)

var defaultSkipDirs = map[string]bool{
	".git":         true,
	".idea":        true,
	".vscode":      true,
	".venv":        true,
	"node_modules": true,
	"vendor":       true,
	"data":         true,
	"dist":         true,
	"build":        true,
	"target":       true,
}

type Input struct {
	Root     string `json:"root" jsonschema:"required,description=项目根目录绝对路径字符串，例如 D:/course/agent/edu.agent.course"` // 需要扫描的项目绝对路径。
	MaxDepth int    `json:"max_depth,omitempty" jsonschema:"description=扫描目录深度，默认 4，最大 8"`                           // 目录树深度，过大会被限制到 maxAllowedDepth。
}

func NewTool() (tool.BaseTool, error) {
	return toolutils.InferTool(
		"project_scan",
		"只读扫描项目目录结构，适合在项目问答或者项目分析中确认模块分层、关键文件和可读取路径，不会执行命令，不会读取文件内容。",
		func(ctx context.Context, input Input) (output string, err error) {
			return doProjectScan(ctx, input)
		},
	)
}

func doProjectScan(ctx context.Context, input Input) (output string, err error) {
	root, err := pathutil.NormalizeExistingRoot(input.Root)
	if err != nil {
		return err.Error(), nil
	}
	workspace := config.GetLatestConfig().WorkSpace
	if err := pathutil.EnsureAllowedByRoot(root, workspace.Root, workspace.EnableEscaped); err != nil {
		return err.Error(), nil
	}
	maxDepth := input.MaxDepth
	if maxDepth <= 0 {
		maxDepth = defaultMaxDepth
	}
	if maxDepth > maxAllowedDepth {
		maxDepth = maxAllowedDepth
	}
	entries, truncated, err := collectEntries(ctx, root, maxDepth)
	if err != nil {
		return err.Error(), nil
	}
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "root=%s max_depth=%d truncated=%t\n\n", input.Root, maxDepth, truncated)
	b.WriteString("--- tree ---\n")
	if len(entries) == 0 {
		b.WriteString("(empty)\n")
		return b.String(), nil
	}
	for _, entry := range entries {
		b.WriteString(entry)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func collectEntries(ctx context.Context, root string, maxDepth int) ([]string, bool, error) {
	var entries []string
	var truncated bool
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
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		parts := splitPath(rel)
		depth := len(parts)
		if d.IsDir() && defaultSkipDirs[d.Name()] {
			return filepath.SkipDir
		}
		if depth >= maxDepth {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if len(entries) > maxProjectScanEntries {
			truncated = true
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		suffix := ""
		if d.IsDir() {
			suffix = "/"
		}
		entries = append(entries, strings.Repeat("  ", depth-1)+"- "+filepath.ToSlash(rel)+suffix)
		return nil
	})
	return entries, truncated, err
}

func splitPath(rel string) []string {
	rel = filepath.Clean(rel)
	if rel == "." || rel == "" {
		return nil
	}
	return strings.Split(rel, string(os.PathSeparator))
}
