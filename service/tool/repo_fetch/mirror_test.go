package repo_fetch

import (
	"reflect"
	"testing"
)

// TestExpandMirrorCandidates 覆盖"配置值 -> 候选列表"的归一化。
//
// 这段逻辑是镜像轮询的入口，配置语义有几处容易踩错的地方：
//   - 留空 不等于 关闭（留空要用默认候选）
//   - "-" 才是关闭
//   - 逗号分隔、空白、缺结尾斜杠都要能正确处理
func TestExpandMirrorCandidates(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		want       []string
	}{
		{
			name:       "留空使用默认候选（多个）",
			configured: "",
			want:       []string{"https://gh-proxy.com/", "https://ghfast.top/", "https://ghproxy.net/"},
		},
		{
			name:       "空白也当留空",
			configured: "   ",
			want:       []string{"https://gh-proxy.com/", "https://ghfast.top/", "https://ghproxy.net/"},
		},
		{
			name:       "单个短横线表示关闭",
			configured: "-",
			want:       nil,
		},
		{
			name:       "单个自定义前缀补结尾斜杠",
			configured: "https://mirror.example.com",
			want:       []string{"https://mirror.example.com/"},
		},
		{
			name:       "多个前缀按顺序保留",
			configured: "https://a.example/,https://b.example/",
			want:       []string{"https://a.example/", "https://b.example/"},
		},
		{
			name:       "空项被丢弃",
			configured: "https://a.example/,,  ,https://b.example",
			want:       []string{"https://a.example/", "https://b.example/"},
		},
		{
			name:       "全部为空项等价于关闭",
			configured: " , , ",
			want:       nil,
		},
		{
			name:       "混入短横线项被丢弃",
			configured: "https://a.example/,-,https://b.example/",
			want:       []string{"https://a.example/", "https://b.example/"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expandMirrorCandidates(tt.configured)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("expandMirrorCandidates(%q)\n got: %#v\nwant: %#v", tt.configured, got, tt.want)
			}
		})
	}
}

// TestResolveMirrorPrefixTakesFirst 确认 resolveMirrorPrefix 只取首项。
func TestResolveMirrorPrefixTakesFirst(t *testing.T) {
	if got := resolveMirrorPrefix("https://a.example/,https://b.example/"); got != "https://a.example/" {
		t.Fatalf("应取首项，实际 %q", got)
	}
	if got := resolveMirrorPrefix("-"); got != "" {
		t.Fatalf("关闭时应为空，实际 %q", got)
	}
}

// TestBuildCloneURLsOrder 覆盖降级顺序：选中的镜像 -> 其余镜像 -> 原始地址。
//
// 这个顺序决定了 cloneRepo 的尝试次序：最快的先试，失败逐个降级，
// 最后一定是直连兜底。若最后一项不是原始地址，"所有镜像都不通"就彻底拉不下来。
func TestBuildCloneURLsOrder(t *testing.T) {
	source := "https://github.com/o/r"
	candidates := []string{"https://a.example/", "https://b.example/", "https://c.example/"}

	t.Run("选中项排最前，其余按配置顺序，直连兜底", func(t *testing.T) {
		got := buildCloneURLs(source, candidates, "https://c.example/")
		want := []string{
			"https://c.example/https://github.com/o/r",
			"https://a.example/https://github.com/o/r",
			"https://b.example/https://github.com/o/r",
			"https://github.com/o/r",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got: %#v\nwant: %#v", got, want)
		}
	})

	t.Run("没有可用镜像时只剩直连", func(t *testing.T) {
		got := buildCloneURLs(source, candidates, "")
		want := []string{
			"https://a.example/https://github.com/o/r",
			"https://b.example/https://github.com/o/r",
			"https://c.example/https://github.com/o/r",
			"https://github.com/o/r",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got: %#v\nwant: %#v", got, want)
		}
	})

	t.Run("关闭镜像时只有原始地址", func(t *testing.T) {
		got := buildCloneURLs(source, nil, "")
		want := []string{"https://github.com/o/r"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got: %#v\nwant: %#v", got, want)
		}
	})

	t.Run("最后一项永远不是镜像地址", func(t *testing.T) {
		got := buildCloneURLs(source, candidates, "https://a.example/")
		if len(got) == 0 {
			t.Fatal("不应为空")
		}
		if got[len(got)-1] != source {
			t.Fatalf("最后一项应为原始地址，实际 %q", got[len(got)-1])
		}
	})

	t.Run("非 github 地址不套镜像", func(t *testing.T) {
		gitlab := "https://gitlab.com/o/r"
		got := buildCloneURLs(gitlab, candidates, "https://a.example/")
		want := []string{"https://gitlab.com/o/r"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got: %#v\nwant: %#v", got, want)
		}
	})

	t.Run("重复地址去重", func(t *testing.T) {
		got := buildCloneURLs(source, []string{"https://a.example/", "https://a.example/"}, "https://a.example/")
		want := []string{
			"https://a.example/https://github.com/o/r",
			"https://github.com/o/r",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got: %#v\nwant: %#v", got, want)
		}
	})
}
