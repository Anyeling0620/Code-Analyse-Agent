package repo_fetch

import (
	"context"
	"net/http"
	"sort"
	"sync"
	"time"

	"edu.agent.code/utils/logger"
)

// mirrorProbeTimeout 是单个镜像探测的超时。
//
// 探测只请求仓库的 info/refs（git smart HTTP 的握手端点），拿到响应头就够判断
// "这条链路通不通、有多快"，不需要真的下载对象。超时给得比较短：真正的
// clone 有 git_clone_timeout_sec（默认 300s）兜着，而测速阶段不该让用户等太久。
const mirrorProbeTimeout = 6 * time.Second

// mirrorProbeParallelism 限制并发探测数，避免一次拉取就打满连接。
const mirrorProbeParallelism = 4

// mirrorProbeResult 记录单个镜像的探测结果。
type mirrorProbeResult struct {
	prefix  string
	latency time.Duration
	ok      bool
}

// pickFastestMirror 并发探测所有候选镜像，返回可用者中延迟最低的那个前缀。
//
// 返回空串表示"没有可用镜像"，调用方应回退到原始地址直连。
//
// 设计取舍：
//   - 每次拉取都重新测速（用户的网络状况会变，缓存结果容易选到已经劣化的镜像）；
//   - 只探测 info/refs 握手端点，不下载对象，所以开销很小；
//   - 任一项探测失败只是一次 HEAD/GET 超时，不影响其它候选；
//   - 全部失败直接返回空串，让调用方走直连，绝不比"配了一个死镜像"更差。
func pickFastestMirror(ctx context.Context, candidates []string, source string) string {
	if len(candidates) == 0 || source == "" {
		return ""
	}
	// 单候选时没有"选最快"的必要，但仍要探测一次：死了就应该直连，
	// 而不是把一条已知不通的镜像地址交给 git 去等超时。
	if len(candidates) == 1 {
		if probeMirror(ctx, candidates[0], source).ok {
			return candidates[0]
		}
		return ""
	}

	results := make([]mirrorProbeResult, len(candidates))
	sem := make(chan struct{}, mirrorProbeParallelism)
	var wg sync.WaitGroup
	for i, prefix := range candidates {
		wg.Add(1)
		go func(idx int, p string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[idx] = probeMirror(ctx, p, source)
		}(i, prefix)
	}
	wg.Wait()

	usable := make([]mirrorProbeResult, 0, len(results))
	for _, r := range results {
		if r.ok {
			usable = append(usable, r)
		}
	}
	if len(usable) == 0 {
		logger.Warn("repo_fetch all mirrors unreachable, fallback to direct source=%s candidates=%d",
			redactCredentials(source), len(candidates))
		return ""
	}
	// 按延迟升序；延迟相同则保持候选原始顺序（sort.SliceStable）。
	sort.SliceStable(usable, func(i, j int) bool {
		return usable[i].latency < usable[j].latency
	})
	best := usable[0]
	logger.Info("repo_fetch mirror selected source=%s mirror=%s latency=%s usable=%d",
		redactCredentials(source), redactCredentials(best.prefix), best.latency, len(usable))
	return best.prefix
}

// probeMirror 测量"从该镜像拉取指定仓库"的握手延迟。
//
// 探测的是 <prefix><source>/info/refs?service=git-upload-pack —— 这正是
// git clone 的第一步，能通过就说明镜像认得这个仓库、链路也是通的。
func probeMirror(ctx context.Context, prefix, source string) mirrorProbeResult {
	result := mirrorProbeResult{prefix: prefix}
	target, mirrored := mirrorGitURL(source, prefix)
	if !mirrored {
		return result
	}
	probeURL := target + "/info/refs?service=git-upload-pack"

	probeCtx, cancel := context.WithTimeout(ctx, mirrorProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, probeURL, nil)
	if err != nil {
		return result
	}
	// 只要响应头和极少量正文：把 Range 限定在 1 字节，避免某些镜像不认 HEAD
	// 而退化成整包传输。
	req.Header.Set("Range", "bytes=0-0")
	req.Header.Set("User-Agent", "git/2.0 (repo_fetch probe)")

	start := time.Now()
	resp, err := probeHTTPClient.Do(req)
	if err != nil {
		return result
	}
	defer func() { _ = resp.Body.Close() }()

	elapsed := time.Since(start)
	// 2xx/3xx 视为可用；206 是 Range 命中的正常返回。
	// 401/403 说明镜像要求鉴权（私有仓库或该镜像不代理此仓库），不算可用。
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		result.ok = true
		result.latency = elapsed
	}
	return result
}

// probeHTTPClient 是探测专用的 HTTP 客户端。
//
// 不复用业务侧客户端：探测要短超时、不重试、不跟随过多跳转，语义不一样。
var probeHTTPClient = &http.Client{
	Timeout: mirrorProbeTimeout,
	// 镜像有时会 302 到实际存储；允许少量跳转。
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return http.ErrUseLastResponse
		}
		return nil
	},
	Transport: &http.Transport{
		Proxy:                 nil, // 探测走直连：镜像本身就是国内加速，套代理反而慢
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   mirrorProbeTimeout,
		ResponseHeaderTimeout: mirrorProbeTimeout,
	},
}

// buildCloneURLs 生成按优先级排序的候选拉取地址。
//
// 顺序：测速选中的镜像 → 其余候选镜像（保持配置顺序）→ 原始地址直连。
// 最后一项永远是 source，保证"所有镜像都不通"时仍能尝试直连，
// 也为 cloneRepo 的逐个降级提供终止条件。重复地址会被去掉。
func buildCloneURLs(source string, candidates []string, chosen string) []string {
	if source == "" {
		return nil
	}
	urls := make([]string, 0, len(candidates)+2)
	seen := make(map[string]bool, len(candidates)+2)
	appendURL := func(u string) {
		if u == "" || seen[u] {
			return
		}
		seen[u] = true
		urls = append(urls, u)
	}

	// 1) 测速最快的镜像优先。
	if chosen != "" {
		if u, ok := mirrorGitURL(source, chosen); ok {
			appendURL(u)
		}
	}
	// 2) 其余候选按配置顺序降级。
	for _, prefix := range candidates {
		if u, ok := mirrorGitURL(source, prefix); ok {
			appendURL(u)
		}
	}
	// 3) 原始地址直连兜底。
	appendURL(source)
	return urls
}
