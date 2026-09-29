package pathutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureAllowedByRoot(t *testing.T) {
	workspace := t.TempDir()
	inside := filepath.Join(workspace, "repos", "demo")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatalf("mkdir inside: %v", err)
	}
	// 与工作区同级的另一个临时目录，必然在工作区之外。
	outside := t.TempDir()
	parent := filepath.Dir(workspace)
	// 目录名前缀相同但并非其子目录，用于验证不是简单字符串前缀匹配。
	siblingByPrefix := workspace + "-evil"
	if err := os.MkdirAll(siblingByPrefix, 0o755); err != nil {
		t.Fatalf("mkdir siblingByPrefix: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(siblingByPrefix) })

	cases := []struct {
		name        string
		root        string
		enableEscap bool
		wantErr     bool
	}{
		{name: "工作区根目录自身", root: workspace, wantErr: false},
		{name: "工作区内子目录", root: inside, wantErr: false},
		{name: "工作区根目录带尾部分隔符", root: workspace + string(os.PathSeparator), wantErr: false},
		{name: "工作区内尚未创建的路径", root: filepath.Join(workspace, "not-yet-created"), wantErr: false},
		{name: "父目录越界", root: parent, wantErr: true},
		{name: "同级目录越界", root: outside, wantErr: true},
		{name: "与前缀相同的兄弟目录越界", root: siblingByPrefix, wantErr: true},
		{name: "enable_escaped 时不限制", root: outside, enableEscap: true, wantErr: false},
		{name: "enable_escaped 时父目录也不限制", root: parent, enableEscap: true, wantErr: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := EnsureAllowedByRoot(tc.root, workspace, tc.enableEscap)
			if tc.wantErr && err == nil {
				t.Fatalf("EnsureAllowedByRoot(%q) = nil, want error", tc.root)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("EnsureAllowedByRoot(%q) = %v, want nil", tc.root, err)
			}
			if tc.wantErr {
				// 错误信息需要能直接提示模型"不能脱离工作区"，便于工具把它当文本返回。
				if !strings.Contains(err.Error(), "不能脱离工作区根目录") {
					t.Fatalf("error = %q, want workspace-escape hint", err.Error())
				}
			}
		})
	}
}

// TestEnsureAllowedByRootRejectsOutOfWorkspace 回归测试：
// 修复前该函数把工作区根目录与自身做 Rel，结果恒为 "."，任何路径都会被放行。
func TestEnsureAllowedByRootRejectsOutOfWorkspace(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()

	if err := EnsureAllowedByRoot(outside, workspace, false); err == nil {
		t.Fatal("工作区之外的路径必须被拒绝，但被放行了")
	}
}

func TestEnsureAllowedByRootResolvesRelativePath(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "repos"), 0o755); err != nil {
		t.Fatalf("mkdir repos: %v", err)
	}
	t.Chdir(workspace)

	if err := EnsureAllowedByRoot("repos", workspace, false); err != nil {
		t.Fatalf("相对路径 repos 应被允许, got %v", err)
	}
	if err := EnsureAllowedByRoot("..", workspace, false); err == nil {
		t.Fatal("相对路径 .. 越界，必须被拒绝")
	}
}
