package repo_fetch

import (
	"net/url"
	"strings"
)

// defaultGitMirrorPrefix 是未配置 workspace.git_mirror_prefix 时使用的候选镜像串。
//
// 单个镜像随时可能失效（实测 ghfast.top 已直接 TLS 握手失败），所以默认值给的是
// 一串候选而不是一个：每次拉取前并发测速，选最快的那个；失败自动降级到下一名。
// 顺序同时是"测速并列时的优先顺序"。
const defaultGitMirrorPrefix = "https://gh-proxy.com/,https://ghfast.top/,https://ghproxy.net/"

// mirrorDisabledValue 是"显式关闭镜像"的配置值：留空代表用默认值，
// 想彻底直连就必须写 "-"，两者语义必须区分开，否则没法关掉镜像。
const mirrorDisabledValue = "-"

// expandMirrorCandidates 把配置值拆成候选前缀列表。
//
//   - 留空        -> 默认候选（逗号分隔的那串）
//   - "-"         -> nil（关闭镜像，直连）
//   - 逗号分隔值  -> 逐项归一化（补结尾 "/"）
//
// 空白项会被丢弃，"a,,b" 等价于 "a,b"；全部为空时返回 nil（等价于关闭）。
func expandMirrorCandidates(configured string) []string {
	value := strings.TrimSpace(configured)
	if value == mirrorDisabledValue {
		return nil
	}
	if value == "" {
		value = defaultGitMirrorPrefix
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		item := strings.TrimSpace(part)
		if item == "" || item == mirrorDisabledValue {
			continue
		}
		if !strings.HasSuffix(item, "/") {
			item += "/"
		}
		out = append(out, item)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// resolveMirrorPrefix 取候选列表的首项，供"不需要测速"的场景使用
// （例如更新已有仓库时的默认入口）。真正拉新仓库请走 pickFastestMirror。
func resolveMirrorPrefix(configured string) string {
	candidates := expandMirrorCandidates(configured)
	if len(candidates) == 0 {
		return ""
	}
	return candidates[0]
}

// mirrorGitURL 把需要加速的远端地址映射成镜像地址，第二个返回值表示是否真的发生了改写。
//
// 只处理 https 的 github.com：ssh / scp / git:// / 非 GitHub 站点一律原样返回，
// 避免把私有仓库或内网地址错误地转发给第三方代理；已经带该前缀的地址也不会二次加前缀。
//
// 注意这里**只影响"用哪个地址去拉代码"**。仓库身份（project_id、目录名、origin）
// 仍然由用户原始地址决定，否则同一个仓库换个入口就会生成两份索引。
func mirrorGitURL(source, prefix string) (string, bool) {
	if prefix == "" || source == "" {
		return source, false
	}
	if strings.HasPrefix(source, prefix) {
		return source, false
	}
	if !strings.HasPrefix(strings.ToLower(source), "https://") {
		return source, false
	}
	parsed, err := url.Parse(source)
	if err != nil {
		return source, false
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "github.com", "www.github.com":
		return prefix + source, true
	default:
		return source, false
	}
}
