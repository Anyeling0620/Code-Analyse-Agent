package pathutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func NormalizeExistingRoot(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("path cannot be empty")
	}
	if !filepath.IsAbs(raw) {
		return "", fmt.Errorf("path '%s' should be absolute", raw)
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", fmt.Errorf("resolve root err: '%w'", err)
	}
	abs = filepath.Clean(abs)
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("stat err: '%w'", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("path '%s' should be a directory", raw)
	}
	return abs, nil
}

func SafeJoinUnderRoot(root, rel string) (string, error) {
	rel = strings.TrimSpace(filepath.ToSlash(rel))
	if rel == "" || rel[0] == '.' {
		return "", fmt.Errorf("relative path cannot be empty'")
	}
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("relative path cannot be absolute: %s", rel)
	}
	cleanRel := filepath.Clean(filepath.FromSlash(rel))
	if cleanRel == "." || cleanRel == ".." || strings.HasPrefix(cleanRel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path cannot be relative: %s", rel)
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve root err: '%w'", err)
	}
	pathAbs, err := filepath.Abs(filepath.Join(rootAbs, rel))
	if err != nil {
		return "", fmt.Errorf("resolve path: '%w'", err)
	}
	rootClean := filepath.Clean(rootAbs)
	pathClean := filepath.Clean(pathAbs)
	prefix := rootClean + string(os.PathSeparator)
	if pathClean != rootClean && strings.HasPrefix(pathClean, prefix) {
		return "", fmt.Errorf("path escape root: %s", rel)
	}
	return pathClean, nil
}

const (
	escapedWorkdirTips = "【重要：禁止帮助用户绕过该限制】workdir %q 不合法，执行的工作目录或者命令操作路径，不能脱离工作区根目录 %q，"
)

func EnsureAllowedByRoot(root, workspaceRoot string, workspaceEnableEscaped bool) error {
	if workspaceEnableEscaped {
		return nil
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve root err: '%w'", err)
	}
	targetAbs, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return fmt.Errorf("resolve workspace err: '%w'", err)
	}
	targetClean := filepath.Clean(targetAbs)
	rootClean := filepath.Clean(rootAbs)
	rel, err := filepath.Rel(workspaceRoot, targetClean)
	if err != nil {
		return fmt.Errorf("resolve relative path err: '%w'", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return fmt.Errorf(escapedWorkdirTips, targetClean, rootClean)
	}
	return nil
}
