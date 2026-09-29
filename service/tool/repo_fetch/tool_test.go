package repo_fetch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"edu.agent.code/config"
	"edu.agent.code/service/projectid"
)

// withTestWorkspace / withTestRunner 是仅供包内单测使用的注入点。
func withTestWorkspace(ws config.WorkSpace) Option {
	return func(o *options) {
		o.workspace = &ws
	}
}

func withTestRunner(runner gitRunner) Option {
	return func(o *options) {
		o.runner = runner
	}
}

// fakeGit 记录被执行的 git 命令，并模拟 clone / rev-parse / config 的返回，
// 让单测完全不依赖网络与真实 git 进程。
type fakeGit struct {
	commands [][]string
	url      string
	commit   string
	branch   string
	onClone  func(dir string) error
	fail     bool
}

func (f *fakeGit) Run(_ context.Context, dir string, _ time.Duration, args ...string) (string, error) {
	f.commands = append(f.commands, append([]string{dir}, args...))
	if len(args) == 0 {
		return "", nil
	}
	switch args[0] {
	case "clone":
		if f.fail {
			return "", fmt.Errorf("模拟 clone 失败")
		}
		target := args[len(args)-1]
		if f.onClone != nil {
			if err := f.onClone(target); err != nil {
				return "", err
			}
		}
		return "", nil
	case "rev-parse":
		if len(args) > 1 && args[1] == "--is-inside-work-tree" {
			return "true", nil
		}
		if len(args) > 1 && args[1] == "--abbrev-ref" {
			return f.branch, nil
		}
		return f.commit, nil
	case "config":
		return f.url, nil
	default:
		return "", nil
	}
}

func (f *fakeGit) hasCommand(name string) bool {
	for _, command := range f.commands {
		for _, arg := range command {
			if arg == name {
				return true
			}
		}
	}
	return false
}

func newOptions(t *testing.T, ws config.WorkSpace, runner gitRunner) *options {
	t.Helper()
	opt := &options{runner: runner}
	withTestWorkspace(ws)(opt)
	return opt
}

func mkdirGitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0755); err != nil {
		t.Fatalf("创建 .git 目录失败: %v", err)
	}
}

func TestClassifySourceRejectsUnsafeInput(t *testing.T) {
	cases := []struct {
		name   string
		source string
	}{
		{name: "ext 传输协议", source: "ext::sh -c whoami"},
		{name: "file 协议", source: "file:///etc/passwd"},
		{name: "以短横线开头", source: "--upload-pack=calc"},
		{name: "双冒号写法", source: "foo::bar"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if _, err := classifySource(item.source); err == nil {
				t.Fatalf("source %q 应该被拒绝", item.source)
			}
		})
	}
}

func TestClassifySourceAcceptsRemoteAndLocal(t *testing.T) {
	remoteCases := []string{
		"https://github.com/owner/repo.git",
		"ssh://git@github.com/owner/repo",
		"git@github.com:owner/repo.git",
	}
	for _, source := range remoteCases {
		if isRemote, err := classifySource(source); err != nil || !isRemote {
			t.Fatalf("source %q 应识别为远端地址，实际 isRemote=%t err=%v", source, isRemote, err)
		}
	}
	localCases := []string{"workspace/repos/demo", `C:\FullStack\Code Analyse Agent\workspace`}
	for _, source := range localCases {
		if isRemote, err := classifySource(source); err != nil || isRemote {
			t.Fatalf("source %q 应识别为本地路径，实际 isRemote=%t err=%v", source, isRemote, err)
		}
	}
}

func TestProjectIDIsStableAcrossCaseAndCloneDirs(t *testing.T) {
	a := projectid.FromRemote("https://github.com/Owner/Repo.git")
	b := projectid.FromRemote("https://github.com/owner/repo")
	if a == "" || a != b {
		t.Fatalf("同一仓库不同写法应得到同一 project_id：%s vs %s", a, b)
	}
	if strings.Contains(a, "github") || strings.Contains(a, "/") {
		t.Fatalf("project_id 必须是可直接用于集合名的安全标识，实际 %s", a)
	}
}

func TestFetchRemoteClonesIntoReposDir(t *testing.T) {
	workspaceRoot := t.TempDir()
	runner := &fakeGit{
		url:    "https://github.com/owner/repo.git",
		commit: "abcdef123456",
		branch: "main",
		onClone: func(dir string) error {
			// 模拟 clone 出的工作区：.git 目录 + 一个源码文件。
			if err := os.MkdirAll(filepath.Join(dir, ".git"), 0755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0644)
		},
	}
	opt := newOptions(t, config.WorkSpace{Root: workspaceRoot}, runner)

	result, err := opt.fetch(context.Background(), Input{Source: "https://github.com/owner/repo.git"})
	if err != nil {
		t.Fatalf("fetch 失败: %v", err)
	}
	reposDir := filepath.Join(workspaceRoot, "repos")
	if !strings.HasPrefix(result.Root, reposDir) {
		t.Fatalf("root 应位于 repos 目录内，实际 %s", result.Root)
	}
	if result.ProjectID != projectid.FromRemote("https://github.com/owner/repo.git") {
		t.Fatalf("project_id 与 remote 派生结果不一致: %s", result.ProjectID)
	}
	if result.Commit != "abcdef123456" || result.Branch != "main" {
		t.Fatalf("commit/branch 解析异常: %+v", result)
	}
	if result.Reused {
		t.Fatalf("首次 clone 不应标记为复用: %+v", result)
	}
	if result.FileCount != 1 {
		t.Fatalf("文件数统计异常: %d", result.FileCount)
	}
	if !runner.hasCommand("clone") {
		t.Fatalf("应执行过 clone，实际命令: %v", runner.commands)
	}

	// clone 目标名必须包含 host/owner/repo 与 project_id，便于人工排查。
	if !strings.Contains(filepath.Base(result.Root), "github.com") ||
		!strings.Contains(filepath.Base(result.Root), "owner") ||
		!strings.Contains(filepath.Base(result.Root), "repo") {
		t.Fatalf("目录名信息量不足: %s", filepath.Base(result.Root))
	}
}

func TestFetchRemoteReusesExistingRepo(t *testing.T) {
	workspaceRoot := t.TempDir()
	runner := &fakeGit{url: "https://github.com/owner/repo.git", commit: "c1", branch: "main"}
	opt := newOptions(t, config.WorkSpace{Root: workspaceRoot}, runner)

	first, err := opt.fetch(context.Background(), Input{Source: "https://github.com/owner/repo.git"})
	if err != nil {
		t.Fatalf("首次 fetch 失败: %v", err)
	}
	// 首次 clone 是 fake 的 onClone 为空 —— 手动补出工作区。
	mkdirGitRepo(t, first.Root)

	runner.commands = nil
	second, err := opt.fetch(context.Background(), Input{Source: "https://github.com/owner/repo.git"})
	if err != nil {
		t.Fatalf("二次 fetch 失败: %v", err)
	}
	if !second.Reused {
		t.Fatalf("已存在仓库应复用: %+v", second)
	}
	if runner.hasCommand("clone") {
		t.Fatalf("复用情况下不应再次 clone: %v", runner.commands)
	}
	if second.Root != first.Root {
		t.Fatalf("同一仓库应落同一目录: %s vs %s", first.Root, second.Root)
	}
}

func TestFetchRemoteRefreshUpdatesRepo(t *testing.T) {
	workspaceRoot := t.TempDir()
	runner := &fakeGit{url: "https://github.com/owner/repo.git", commit: "c2", branch: "main"}
	opt := newOptions(t, config.WorkSpace{Root: workspaceRoot}, runner)

	first, err := opt.fetch(context.Background(), Input{Source: "https://github.com/owner/repo.git"})
	if err != nil {
		t.Fatalf("首次 fetch 失败: %v", err)
	}
	mkdirGitRepo(t, first.Root)

	runner.commands = nil
	if _, err := opt.fetch(context.Background(), Input{Source: "https://github.com/owner/repo.git", Refresh: true}); err != nil {
		t.Fatalf("刷新 fetch 失败: %v", err)
	}
	if !runner.hasCommand("fetch") || !runner.hasCommand("reset") {
		t.Fatalf("refresh=true 应执行 fetch + reset: %v", runner.commands)
	}
}

func TestFetchRemoteRebuildsBrokenDirectory(t *testing.T) {
	workspaceRoot := t.TempDir()
	runner := &fakeGit{
		url: "https://github.com/owner/repo.git",
		onClone: func(dir string) error {
			if err := os.MkdirAll(filepath.Join(dir, ".git"), 0755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(dir, "fresh.go"), []byte("package main\n"), 0644)
		},
	}
	opt := newOptions(t, config.WorkSpace{Root: workspaceRoot}, runner)

	// 先用一次失败的 clone 留下半成品目录。
	expectedDir := filepath.Join(workspaceRoot, "repos", fmt.Sprintf("github.com-owner-repo-%s",
		projectid.FromRemote("https://github.com/owner/repo.git")))
	if err := os.MkdirAll(expectedDir, 0755); err != nil {
		t.Fatalf("准备半成品目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(expectedDir, "leftover.txt"), []byte("stale"), 0644); err != nil {
		t.Fatalf("准备残留文件失败: %v", err)
	}

	result, err := opt.fetch(context.Background(), Input{Source: "https://github.com/owner/repo.git"})
	if err != nil {
		t.Fatalf("fetch 失败: %v", err)
	}
	if result.Reused {
		t.Fatalf("半成品目录不应被复用: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(result.Root, "leftover.txt")); err == nil {
		t.Fatalf("半成品残留文件应被清理")
	}
	if !runner.hasCommand("clone") {
		t.Fatalf("应重新 clone: %v", runner.commands)
	}
}

func TestFetchRemoteCleansUpOversizedRepo(t *testing.T) {
	workspaceRoot := t.TempDir()
	runner := &fakeGit{
		url: "https://github.com/owner/repo.git",
		onClone: func(dir string) error {
			if err := os.MkdirAll(filepath.Join(dir, ".git"), 0755); err != nil {
				return err
			}
			// 2MB 文件 + 1MB 上限 = 必须被清理。
			return os.WriteFile(filepath.Join(dir, "big.bin"), make([]byte, 2*1024*1024), 0644)
		},
	}
	opt := newOptions(t, config.WorkSpace{Root: workspaceRoot, MaxRepoMB: 1}, runner)

	_, err := opt.fetch(context.Background(), Input{Source: "https://github.com/owner/repo.git"})
	if err == nil {
		t.Fatalf("超过体积上限应报错")
	}
	if !strings.Contains(err.Error(), "超过上限") {
		t.Fatalf("错误信息应说明体积超限，实际: %v", err)
	}
	entries, readErr := os.ReadDir(filepath.Join(workspaceRoot, "repos"))
	if readErr != nil {
		t.Fatalf("读取 repos 目录失败: %v", readErr)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "repo") {
			t.Fatalf("超限仓库应被清理，仍存在目录 %s", entry.Name())
		}
	}
}

func TestResolveLocalRelativeAndAbsolute(t *testing.T) {
	workspaceRoot := t.TempDir()
	projectDir := filepath.Join(workspaceRoot, "demo")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("准备项目目录失败: %v", err)
	}
	runner := &fakeGit{}
	opt := newOptions(t, config.WorkSpace{Root: workspaceRoot}, runner)

	relative, err := opt.fetch(context.Background(), Input{Source: "demo"})
	if err != nil {
		t.Fatalf("相对路径解析失败: %v", err)
	}
	if relative.Root != projectDir {
		t.Fatalf("相对路径应相对 workspace.root 解析，实际 %s", relative.Root)
	}
	if relative.ProjectID == "" {
		t.Fatalf("本地目录也应派生 project_id")
	}

	absolute, err := opt.fetch(context.Background(), Input{Source: projectDir})
	if err != nil {
		t.Fatalf("绝对路径解析失败: %v", err)
	}
	if absolute.Root != projectDir || absolute.ProjectID != relative.ProjectID {
		t.Fatalf("同一目录两种写法应得到同一 project_id: %+v vs %+v", relative, absolute)
	}
}

func TestResolveLocalRejectsPathOutsideWorkspace(t *testing.T) {
	workspaceRoot := t.TempDir()
	outside := t.TempDir()
	opt := newOptions(t, config.WorkSpace{Root: workspaceRoot}, &fakeGit{})

	if _, err := opt.fetch(context.Background(), Input{Source: outside}); err == nil {
		t.Fatalf("工作区外的绝对路径必须被拒绝")
	}
	if _, err := opt.fetch(context.Background(), Input{Source: "../escape"}); err == nil {
		t.Fatalf("越界的相对路径必须被拒绝")
	}
}

func TestHooksReceiveResult(t *testing.T) {
	workspaceRoot := t.TempDir()
	projectDir := filepath.Join(workspaceRoot, "demo")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("准备项目目录失败: %v", err)
	}
	var got Result
	opt := &options{runner: &fakeGit{}, hooks: Hooks{
		OnReady: func(_ context.Context, r Result) {
			got = r
		},
	}}
	withTestWorkspace(config.WorkSpace{Root: workspaceRoot})(opt)

	if _, err := opt.handle(context.Background(), Input{Source: "demo"}); err != nil {
		t.Fatalf("handle 失败: %v", err)
	}
	if got.Root != projectDir || got.ProjectID == "" {
		t.Fatalf("OnReady 未收到正确的回执: %+v", got)
	}
}

func TestRedactCredentials(t *testing.T) {
	input := "fatal: could not read from https://user:ghp_secrettoken@github.com/owner/repo.git"
	output := redactCredentials(input)
	if strings.Contains(output, "ghp_secrettoken") || strings.Contains(output, "user:") {
		t.Fatalf("凭证未被脱敏: %s", output)
	}
	if !strings.Contains(output, "https://***@github.com") {
		t.Fatalf("脱敏结果格式异常: %s", output)
	}
}

func TestSanitizePathSegment(t *testing.T) {
	cases := map[string]string{
		"github.com": "github.com",
		"owner/repo": "owner-repo",
		"":           "repo",
		"..":         "repo",
		"a b":        "a-b",
	}
	for input, want := range cases {
		if got := sanitizePathSegment(input); got != want {
			t.Fatalf("sanitizePathSegment(%q) = %q, 期望 %q", input, got, want)
		}
	}
}
