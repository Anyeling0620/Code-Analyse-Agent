package repo_fetch

import (
	"net/url"
	"strings"
)

// defaultGitMirrorPrefix 是未配置 workspace.git_mirror_prefix 时使用的默认加速前缀。
const defaultGitMirrorPrefix = "https://ghfast.top/"

// mirrorDisabledValue 是"显式关闭镜像"的配置值：留空代表用默认值，
// 想彻底直连就必须写 "-"，两者语义必须区分开，否则没法关掉镜像。
const mirrorDisabledValue = "-"

// resolveMirrorPrefix 把配置值归一化成可以直接拼接的 URL 前缀。
//
//   - 留空      -> defaultGitMirrorPrefix
//   - "-"       -> ""（关闭镜像）
//   - 其它值    -> 补上结尾的 "/" 后使用
func resolveMirrorPrefix(configured string) string {
	value := strings.TrimSpace(configured)
	if value == mirrorDisabledValue {
		return ""
	}
	if value == "" {
		value = defaultGitMirrorPrefix
	}
	if !strings.HasSuffix(value, "/") {
		value += "/"
	}
	return value
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
