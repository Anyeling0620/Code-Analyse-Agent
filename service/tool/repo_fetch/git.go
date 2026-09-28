package repo_fetch

import (
	"context"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// gitRunner 抽象 git 命令执行，便于在单测里替换掉真实网络与 git 进程。
type gitRunner interface {
	Run(ctx context.Context, dir string, timeout time.Duration, args ...string) (string, error)
}

// execGitRunner 是生产实现：直接调用系统 git，不经过 terminal 工具
// （terminal 工具存在 60 秒硬上限，真实仓库 clone 必然超时）。
type execGitRunner struct{}

func (execGitRunner) Run(ctx context.Context, dir string, timeout time.Duration, args ...string) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if runCtx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("git 命令超时（超过 %s）", timeout)
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("git %s 执行失败：%s", args[0], redactCredentials(detail))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// credentialPattern 匹配 URL 中内嵌的凭证（https://user:token@host）。
var credentialPattern = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^/@\s]+@`)

// redactCredentials 脱敏 URL 内嵌凭证，避免 token 进入日志与模型上下文。
func redactCredentials(text string) string {
	return credentialPattern.ReplaceAllString(text, "${1}***@")
}

// forbiddenTransportPrefixes 是明确拒绝的 git 传输协议。
// ext:: 可以在 clone 时执行任意命令，file:// 会突破工作区边界读取本机文件。
var forbiddenTransportPrefixes = []string{"ext::", "file://", "fd::", "remote-ext::"}

// classifySource 判断 source 是远端 git 地址还是本地路径，并做安全校验。
func classifySource(source string) (isRemote bool, err error) {
	if source == "" {
		return false, fmt.Errorf("source 不能为空")
	}
	if strings.HasPrefix(source, "-") {
		return false, fmt.Errorf("source 不能以 - 开头，避免被当作 git 命令选项")
	}
	lower := strings.ToLower(source)
	for _, prefix := range forbiddenTransportPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return false, fmt.Errorf("不支持的 git 传输协议：%s", prefix)
		}
	}
	// 只拦截双冒号形式，避免误伤 Windows 盘符（C:\...）。
	if strings.Contains(lower, "::") {
		return false, fmt.Errorf("不支持的 git 传输协议写法，source 中不允许出现 ::")
	}
	if isRemoteGitURL(source) {
		return true, nil
	}
	return false, nil
}

// isRemoteGitURL 识别常见的 git 地址写法。
func isRemoteGitURL(source string) bool {
	lower := strings.ToLower(source)
	for _, prefix := range []string{"http://", "https://", "ssh://", "git://"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	// scp 风格：git@github.com:owner/repo.git
	if index := strings.Index(source, "@"); index > 0 {
		rest := source[index+1:]
		host, repoPath, ok := strings.Cut(rest, ":")
		if ok && host != "" && repoPath != "" && !strings.ContainsAny(host, `/\`) && !strings.Contains(repoPath, `\`) {
			return true
		}
	}
	return false
}

// remoteParts 把 git 地址拆成 host / owner / repo，用于生成稳定的目录名。
func remoteParts(remoteURL string) (host, owner, repo string) {
	raw := remoteURL
	if !strings.Contains(raw, "://") {
		if index := strings.Index(raw, "@"); index > 0 {
			raw = "ssh://" + raw
		}
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "", "", ""
	}
	host = parsed.Hostname()
	repoPath := strings.Trim(parsed.Path, "/")
	segments := strings.Split(repoPath, "/")
	if len(segments) > 0 {
		repo = strings.TrimSuffix(segments[len(segments)-1], ".git")
	}
	if len(segments) > 1 {
		owner = strings.Join(segments[:len(segments)-1], "-")
	}
	return host, owner, repo
}

// sanitizePathSegment 让目录名只保留安全字符，防止远端地址影响本地路径。
func sanitizePathSegment(value string) string {
	var builder strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			builder.WriteRune(r)
		default:
			builder.WriteRune('-')
		}
	}
	cleaned := strings.Trim(builder.String(), "-")
	if cleaned == "" || cleaned == "." || cleaned == ".." {
		return "repo"
	}
	return cleaned
}

// isGitRepository 判断目录是否是一个可用的 git 工作区（能解析出 HEAD）。
func isGitRepository(ctx context.Context, runner gitRunner, dir string) bool {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return false
	}
	if _, err := runner.Run(ctx, dir, 30*time.Second, "rev-parse", "--is-inside-work-tree"); err != nil {
		return false
	}
	return true
}

// headCommit 返回当前 HEAD 的 commit，失败返回空串。
func headCommit(ctx context.Context, runner gitRunner, dir string, timeout time.Duration) string {
	out, err := runner.Run(ctx, dir, timeout, "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// headBranch 返回当前分支名；detached HEAD 时返回空串。
func headBranch(ctx context.Context, runner gitRunner, dir string, timeout time.Duration) string {
	out, err := runner.Run(ctx, dir, timeout, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return ""
	}
	out = strings.TrimSpace(out)
	if out == "HEAD" {
		return ""
	}
	return out
}

// describeRemote 读回仓库的 origin 地址（可能被 git 规范化过），用于派生 project_id。
func describeRemote(ctx context.Context, runner gitRunner, dir string, timeout time.Duration) string {
	out, err := runner.Run(ctx, dir, timeout, "config", "--get", "remote.origin.url")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// summarizeRepo 统计仓库体积与文件数，用于体积上限校验与结果回执。
func summarizeRepo(ctx context.Context, root string) (sizeMB float64, fileCount int, err error) {
	var totalBytes int64
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}
		totalBytes += info.Size()
		fileCount++
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return float64(totalBytes) / (1024 * 1024), fileCount, nil
}
