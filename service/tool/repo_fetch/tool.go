// Package repo_fetch 提供"把待分析项目准备到工作区内"的工具。
//
// 它把用户给出的 git 地址或本地路径统一解析成一个稳定的本地项目根目录，
// 并把该目录、commit、project_id 交给下游（repo_analyzer 与项目级 RAG 索引）。
// 之前这件事只能由模型自己调 terminal 执行 git clone，但 terminal 存在 60 秒
// 硬上限，真实仓库必然超时，且 clone 目录名不固定会让语义索引无法复用。
package repo_fetch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"edu.agent.code/config"
	"edu.agent.code/service/projectid"
	"edu.agent.code/utils/logger"
	"edu.agent.code/utils/pathutil"

	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
)

const (
	// ToolName 是暴露给模型的工具名。
	ToolName = "repo_fetch"

	defaultGitTimeoutSec = 300
	defaultMaxRepoMB     = 2048

	// maxRepoFileCount 是文件数上限，避免把极端仓库拖进分析流程。
	maxRepoFileCount = 200000
)

// Result 是仓库准备完成后的回执，同时通过 Hooks 传给索引与会话回写逻辑。
type Result struct {
	Root      string  `json:"root"`
	ProjectID string  `json:"project_id"`
	Remote    string  `json:"remote,omitempty"`
	Commit    string  `json:"commit,omitempty"`
	Branch    string  `json:"branch,omitempty"`
	Reused    bool    `json:"reused"`
	SizeMB    float64 `json:"size_mb"`
	FileCount int     `json:"file_count"`
}

// Hooks 由集成层注入。OnReady 是同步调用，实现方必须自己起 goroutine 做异步工作，
// 不能阻塞 repo_fetch 的返回。
type Hooks struct {
	OnReady func(ctx context.Context, r Result)
}

// Input 是工具入参。
type Input struct {
	Source  string `json:"source" jsonschema:"required,description=git 仓库地址（https://、ssh://、git@host:owner/repo）或本机路径。本机路径可以是绝对路径，也可以是相对工作区根目录的相对路径，例如 workspace/repos/my-repo；本地调试时用相对路径更省事。"`
	Ref     string `json:"ref,omitempty" jsonschema:"description=可选，需要分析的分支、tag 或 commit。留空表示使用默认分支。"`
	Refresh bool   `json:"refresh,omitempty" jsonschema:"description=可选，仓库已存在时是否拉取最新代码。默认 false 复用已有副本；只有用户明确要求看最新代码时才传 true。"`
}

type options struct {
	hooks Hooks
	// workspace 仅用于测试注入；为空时读全局配置。
	workspace *config.WorkSpace
	runner    gitRunner
}

// Option 用于在构造工具时注入依赖。
type Option func(*options)

// WithHooks 注入仓库就绪后的联动逻辑（异步建索引、回写会话项目上下文）。
func WithHooks(h Hooks) Option {
	return func(o *options) {
		o.hooks = h
	}
}

// NewTool 构造 repo_fetch 工具。
func NewTool(opts ...Option) (tool.BaseTool, error) {
	opt := &options{runner: execGitRunner{}}
	for _, apply := range opts {
		apply(opt)
	}
	return toolutils.InferTool(
		ToolName,
		"把待分析的代码仓库准备到工作区内，返回稳定的本地绝对路径（root）。"+
			"用户给出 git 地址（如 https://github.com/owner/repo）或本机路径时，都必须先用本工具取得 root，"+
			"再把 root 交给 repo_analyzer 或 project_qa；不要自己用 terminal 执行 git clone。",
		func(ctx context.Context, input Input) (string, error) {
			return opt.handle(ctx, input)
		},
	)
}

func (o *options) handle(ctx context.Context, input Input) (string, error) {
	result, err := o.fetch(ctx, input)
	if err != nil {
		return "仓库准备失败：" + err.Error(), nil
	}
	if o.hooks.OnReady != nil {
		o.hooks.OnReady(ctx, result)
	}
	return formatResult(result), nil
}

// workspaceConf 读取并补齐工作区配置默认值。
func (o *options) workspaceConf() config.WorkSpace {
	var workspace config.WorkSpace
	if o.workspace != nil {
		workspace = *o.workspace
	} else {
		workspace = config.GetLatestConfig().WorkSpace
	}
	if workspace.ReposDir == "" && workspace.Root != "" {
		workspace.ReposDir = filepath.Join(workspace.Root, "repos")
	}
	if workspace.GitCloneTimeoutSec <= 0 {
		workspace.GitCloneTimeoutSec = defaultGitTimeoutSec
	}
	if workspace.MaxRepoMB <= 0 {
		workspace.MaxRepoMB = defaultMaxRepoMB
	}
	return workspace
}

func (o *options) timeout(workspace config.WorkSpace) time.Duration {
	return time.Duration(workspace.GitCloneTimeoutSec) * time.Second
}

// fetch 是工具的核心：把 source 归一化成工作区内的项目根目录。
func (o *options) fetch(ctx context.Context, input Input) (Result, error) {
	workspace := o.workspaceConf()
	source := strings.TrimSpace(input.Source)
	isRemote, err := classifySource(source)
	if err != nil {
		return Result{}, err
	}
	if isRemote {
		return o.fetchRemote(ctx, workspace, source, strings.TrimSpace(input.Ref), input.Refresh)
	}
	return o.resolveLocal(ctx, workspace, source)
}

// fetchRemote 把远端仓库 clone 到 <repos_dir>/<host>-<owner>-<repo>-<projectID>。
func (o *options) fetchRemote(ctx context.Context, workspace config.WorkSpace, source, ref string, refresh bool) (Result, error) {
	if workspace.Root == "" {
		return Result{}, fmt.Errorf("workspace.root 未配置，无法确定工作区范围")
	}
	reposDir, err := filepath.Abs(workspace.ReposDir)
	if err != nil {
		return Result{}, fmt.Errorf("解析 repos_dir 失败：%w", err)
	}
	if err := os.MkdirAll(reposDir, 0755); err != nil {
		return Result{}, fmt.Errorf("创建 repos_dir %s 失败：%w", reposDir, err)
	}

	projectID := projectid.FromRemote(source)
	if projectID == "" {
		return Result{}, fmt.Errorf("无法从 %s 解析仓库标识", redactCredentials(source))
	}
	host, owner, repo := remoteParts(source)
	dirName := strings.Join([]string{
		sanitizePathSegment(host),
		sanitizePathSegment(owner),
		sanitizePathSegment(repo),
		projectID,
	}, "-")
	target := filepath.Join(reposDir, dirName)
	if err := ensureUnderDir(target, reposDir); err != nil {
		return Result{}, err
	}

	unlock := lockRepoDir(target)
	defer unlock()

	timeout := o.timeout(workspace)
	reused := false
	switch {
	case isGitRepository(ctx, o.runner, target):
		reused = true
		if refresh {
			if err := o.updateRepo(ctx, target, ref, timeout); err != nil {
				return Result{}, err
			}
		} else {
			logger.Info("repo_fetch reuse existing repository dir=%s", target)
		}
	default:
		if _, statErr := os.Stat(target); statErr == nil {
			// 目录存在但不是可用仓库：多半是上次 clone 中断留下的半成品，清掉重建。
			logger.Warn("repo_fetch remove broken repo dir=%s", target)
			if err := os.RemoveAll(target); err != nil {
				return Result{}, fmt.Errorf("清理半成品目录失败：%w", err)
			}
		}
		if err := o.cloneRepo(ctx, source, target, ref, timeout); err != nil {
			return Result{}, err
		}
	}

	sizeMB, fileCount, err := summarizeRepo(ctx, target)
	if err != nil {
		return Result{}, fmt.Errorf("统计仓库体积失败：%w", err)
	}
	if sizeMB > float64(workspace.MaxRepoMB) {
		if removeErr := os.RemoveAll(target); removeErr != nil {
			logger.Error("repo_fetch cleanup oversized repo failed dir=%s err=%v", target, removeErr)
		}
		return Result{}, fmt.Errorf("仓库体积 %.1fMB 超过上限 %dMB，已清理本地副本；请换用更小的仓库或调大 workspace.max_repo_mb", sizeMB, workspace.MaxRepoMB)
	}
	if fileCount > maxRepoFileCount {
		if removeErr := os.RemoveAll(target); removeErr != nil {
			logger.Error("repo_fetch cleanup oversized repo failed dir=%s err=%v", target, removeErr)
		}
		return Result{}, fmt.Errorf("仓库文件数 %d 超过上限 %d，已清理本地副本", fileCount, maxRepoFileCount)
	}

	remote := describeRemote(ctx, o.runner, target, timeout)
	if remote == "" {
		remote = source
	}
	return Result{
		Root:      target,
		ProjectID: projectID,
		Remote:    redactCredentials(remote),
		Commit:    headCommit(ctx, o.runner, target, timeout),
		Branch:    headBranch(ctx, o.runner, target, timeout),
		Reused:    reused,
		SizeMB:    roundMB(sizeMB),
		FileCount: fileCount,
	}, nil
}

func (o *options) cloneRepo(ctx context.Context, source, target, ref string, timeout time.Duration) error {
	args := []string{"clone", "--depth=1", "--single-branch"}
	if ref != "" {
		args = append(args, "--branch", ref)
	}
	// "--" 之后一律按位置参数处理，避免 source 被当作 git 选项。
	args = append(args, "--", source, target)
	if _, err := o.runner.Run(ctx, "", timeout, args...); err != nil {
		// clone 失败可能留下空目录，清掉避免下次被误判为已有仓库。
		_ = os.RemoveAll(target)
		return fmt.Errorf("克隆仓库失败（%s）：%w", redactCredentials(source), err)
	}
	return nil
}

// updateRepo 刷新已有仓库：默认分支走 fetch + 硬重置，指定 ref 时切到 FETCH_HEAD。
func (o *options) updateRepo(ctx context.Context, target, ref string, timeout time.Duration) error {
	fetchArgs := []string{"fetch", "--depth=1", "--force", "origin"}
	if ref != "" {
		fetchArgs = append(fetchArgs, ref)
	}
	if _, err := o.runner.Run(ctx, target, timeout, fetchArgs...); err != nil {
		return fmt.Errorf("更新仓库失败：%w", err)
	}
	if ref != "" {
		if _, err := o.runner.Run(ctx, target, timeout, "checkout", "--force", "--detach", "FETCH_HEAD"); err != nil {
			return fmt.Errorf("切换到 %s 失败：%w", ref, err)
		}
		return nil
	}
	if _, err := o.runner.Run(ctx, target, timeout, "reset", "--hard", "FETCH_HEAD"); err != nil {
		return fmt.Errorf("更新仓库失败：%w", err)
	}
	return nil
}

// resolveLocal 处理本机路径：相对路径按工作区根目录解析，绝对路径必须落在工作区内。
func (o *options) resolveLocal(ctx context.Context, workspace config.WorkSpace, source string) (Result, error) {
	if source == "" {
		return Result{}, fmt.Errorf("source 不能为空")
	}
	raw := source
	if !filepath.IsAbs(raw) {
		if workspace.Root == "" {
			return Result{}, fmt.Errorf("workspace.root 未配置，无法解析相对路径 %s", source)
		}
		raw = filepath.Join(workspace.Root, filepath.FromSlash(source))
	}
	if err := ensureWithinWorkspace(raw, workspace.Root, workspace.EnableEscaped); err != nil {
		return Result{}, err
	}
	root, err := pathutil.NormalizeExistingRoot(raw)
	if err != nil {
		return Result{}, fmt.Errorf("本地路径不可用：%w", err)
	}

	sizeMB, fileCount, err := summarizeRepo(ctx, root)
	if err != nil {
		return Result{}, fmt.Errorf("统计目录体积失败：%w", err)
	}
	remote := describeRemote(ctx, o.runner, root, o.timeout(workspace))
	result := Result{
		Root:      root,
		ProjectID: projectid.Derive(root, remote),
		Remote:    redactCredentials(remote),
		Reused:    true,
		SizeMB:    roundMB(sizeMB),
		FileCount: fileCount,
	}
	if isGitRepository(ctx, o.runner, root) {
		result.Commit = headCommit(ctx, o.runner, root, o.timeout(workspace))
		result.Branch = headBranch(ctx, o.runner, root, o.timeout(workspace))
	}
	return result, nil
}

func formatResult(result Result) string {
	var builder strings.Builder
	builder.WriteString("仓库已就绪：")
	builder.WriteString("\nroot=")
	builder.WriteString(result.Root)
	builder.WriteString("\nproject_id=")
	builder.WriteString(result.ProjectID)
	if result.Remote != "" {
		builder.WriteString("\nremote=")
		builder.WriteString(result.Remote)
	}
	if result.Commit != "" {
		builder.WriteString("\ncommit=")
		builder.WriteString(result.Commit)
	}
	if result.Branch != "" {
		builder.WriteString("\nbranch=")
		builder.WriteString(result.Branch)
	}
	builder.WriteString(fmt.Sprintf("\nreused=%t", result.Reused))
	builder.WriteString(fmt.Sprintf("\nsize_mb=%.2f", result.SizeMB))
	builder.WriteString(fmt.Sprintf("\nfile_count=%d", result.FileCount))
	builder.WriteString("\n\n下一步：用上面的 root 作为本地项目路径继续分析；系统已在后台为该仓库建立语义索引。")
	return builder.String()
}

func roundMB(sizeMB float64) float64 {
	return float64(int64(sizeMB*100+0.5)) / 100
}

// ensureUnderDir 校验 child 位于 parent 之内，防止清理操作越界。
func ensureUnderDir(child, parent string) error {
	rel, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
	if err != nil {
		return fmt.Errorf("解析目录关系失败：%w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("目标目录 %s 不在仓库目录 %s 内，已拒绝", child, parent)
	}
	return nil
}

// ensureWithinWorkspace 校验目标路径确实位于工作区根目录之内。
//
// 这里刻意不复用 pathutil.EnsureAllowedByRoot：该函数当前把工作区根目录与它自身做
// 相对路径比较，结果恒为 "."，因而对任何路径都会放行。repo_fetch 需要真实的边界校验
// （本地路径形态允许直接指向磁盘上任意目录），因此在包内做正确判断。
func ensureWithinWorkspace(path, workspaceRoot string, enableEscaped bool) error {
	if enableEscaped {
		return nil
	}
	if strings.TrimSpace(workspaceRoot) == "" {
		return fmt.Errorf("workspace.root 未配置，无法确认路径是否位于工作区内")
	}
	rootAbs, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return fmt.Errorf("解析工作区根目录失败：%w", err)
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("解析目标路径失败：%w", err)
	}
	rootAbs = filepath.Clean(rootAbs)
	pathAbs = filepath.Clean(pathAbs)
	rel, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil {
		return fmt.Errorf("解析路径关系失败：%w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("路径 %s 超出工作区根目录 %s，已拒绝；如确需分析工作区外的目录，请把项目放在工作区内或开启 workspace.enable_escaped", pathAbs, rootAbs)
	}
	return nil
}

var (
	repoDirLocksMu sync.Mutex
	repoDirLocks   = map[string]*sync.Mutex{}
)

// lockRepoDir 对单个目标目录加在途锁：多个会话同时请求同一仓库时只跑一次 clone。
func lockRepoDir(target string) func() {
	key := filepath.Clean(target)
	if os.PathSeparator == '\\' {
		key = strings.ToLower(key)
	}
	repoDirLocksMu.Lock()
	mutex, ok := repoDirLocks[key]
	if !ok {
		mutex = &sync.Mutex{}
		repoDirLocks[key] = mutex
	}
	repoDirLocksMu.Unlock()

	mutex.Lock()
	return mutex.Unlock
}
