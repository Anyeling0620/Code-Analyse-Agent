// Package projectid 提供项目唯一标识的派生逻辑。
//
// 同一个仓库即使被 clone 到不同目录，只要 remote 一致，就会得到相同的 ID；
// 非 git 目录退化为按本地绝对路径派生。该 ID 用于隔离向量集合与会话上下文，
// 因此规则必须全局唯一、稳定、可复现。
package projectid

import (
	"crypto/sha1"
	"encoding/hex"
	"net/url"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// scpLikePattern 匹配 git 的 scp 风格地址：git@github.com:owner/repo.git
var scpLikePattern = regexp.MustCompile(`^(?:[^@/]+@)?([^:/@]+):(.+)$`)

// knownMirrorHosts 是已知的 GitHub 加速镜像域名。
//
// 镜像地址形如 https://ghfast.top/https://github.com/owner/repo：url.Parse 会把镜像域名
// 当成 host、把真实地址整段塞进 path，直接哈希会得到与原始地址完全不同的 id，
// 于是同一个仓库在"直连"和"走镜像"两种写法下会各自建一套索引。这里先剥壳再归一化。
var knownMirrorHosts = map[string]bool{
	"ghfast.top":         true,
	"gh-proxy.com":       true,
	"ghproxy.net":        true,
	"ghproxy.com":        true,
	"mirror.ghproxy.com": true,
}

const idLength = 16

// Derive 派生项目标识：remote 可用时优先用 remote，否则用本地绝对路径。
func Derive(root, remote string) string {
	if id := FromRemote(remote); id != "" {
		return id
	}
	return FromRoot(root)
}

// FromRemote 从 git remote 派生；remote 为空或无法识别时返回空串。
func FromRemote(remote string) string {
	normalized := normalizeRemote(remote)
	if normalized == "" {
		return ""
	}
	return hash(normalized)
}

// FromRoot 从本地路径派生；Windows 下大小写不敏感。
func FromRoot(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return ""
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	abs = filepath.Clean(abs)
	abs = filepath.ToSlash(abs)
	abs = strings.TrimSuffix(abs, "/")
	if runtime.GOOS == "windows" {
		abs = strings.ToLower(abs)
	}
	return hash(abs)
}

// normalizeRemote 把各种 git 地址写法归一化成 host/path 形式，便于稳定哈希。
func normalizeRemote(remote string) string {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return ""
	}
	remote = unwrapMirrorRemote(remote)
	// 去掉可能的凭证，避免不同 token 产生不同 ID，也避免泄露。
	if strings.Contains(remote, "://") {
		parsed, err := url.Parse(remote)
		if err != nil {
			return ""
		}
		host := parsed.Hostname()
		path := parsed.Path
		if host == "" {
			return ""
		}
		return cleanHostPath(host, path)
	}
	if matches := scpLikePattern.FindStringSubmatch(remote); len(matches) == 3 {
		return cleanHostPath(matches[1], matches[2])
	}
	return ""
}

// unwrapMirrorRemote 剥掉已知镜像前缀，返回内层真实地址；不是镜像地址时原样返回。
//
// 只认"host 是已知镜像域名 + path 是带 scheme 的完整地址"这一种形态，
// 不做通用递归解包，避免把正常路径误当成嵌套 URL。
func unwrapMirrorRemote(remote string) string {
	parsed, err := url.Parse(remote)
	if err != nil || parsed.Host == "" {
		return remote
	}
	if !knownMirrorHosts[strings.ToLower(parsed.Hostname())] {
		return remote
	}
	inner := strings.TrimPrefix(parsed.Path, "/")
	lower := strings.ToLower(inner)
	if !strings.HasPrefix(lower, "https://") && !strings.HasPrefix(lower, "http://") {
		return remote
	}
	return inner
}

func cleanHostPath(host, path string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	path = strings.TrimSpace(path)
	path = strings.TrimSuffix(path, "/")
	path = strings.TrimSuffix(path, ".git")
	path = strings.Trim(path, "/")
	if host == "" || path == "" {
		return ""
	}
	return host + "/" + strings.ToLower(path)
}

func hash(value string) string {
	if value == "" {
		return ""
	}
	sum := sha1.Sum([]byte(value))
	return "p" + hex.EncodeToString(sum[:])[:idLength]
}
