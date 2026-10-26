#!/usr/bin/env bash
#
# 生成 GitHub Release 的说明文本。
#
# 背景：之前 Release 页面只有 commit/run/tag 三行技术信息，用户点进去看不出
# 这一版到底改了什么。这里自动从 git 历史生成"提交清单 + 变更规模 + 部署信息"。
#
# 用法：
#   release-notes.sh <sha> <current_tag> <rolling_tag> <run_number>
#
# 输出：标准输出即完整的 Markdown 说明，直接喂给 gh release edit/create --notes。
#
# 设计要点：
#   - 需要在仓库根目录、且有完整 git 历史（CI 里靠 actions/checkout fetch-depth: 0）；
#   - 找不到上一个 release tag 时不报错，退化为"只列本次提交"；
#   - 提交按 feat / fix / 其它归成三节，空节自动省略，避免出现空标题。

set -Eeuo pipefail

SHA="${1:-${GITHUB_SHA:-HEAD}}"
CURRENT_TAG="${2:-${CURRENT_RELEASE_TAG:-release-unknown}}"
ROLLING_TAG="${3:-${ROLLING_RELEASE_TAG:-release-latest}}"
RUN_NUMBER="${4:-${GITHUB_RUN_NUMBER:-local}}"

short_sha="${SHA:0:12}"

# ── 找上一个 release tag ──────────────────────────────────────────────
# 用 describe --match 限定只看 release-* 标签，避免把业务 tag 当成版本基线。
prev_tag=""
if git rev-parse --git-dir >/dev/null 2>&1; then
  prev_tag="$(git describe --tags --abbrev=0 --match 'release-*' "${SHA}^" 2>/dev/null || true)"
fi

if [ -n "$prev_tag" ]; then
  range="${prev_tag}..${SHA}"
  range_desc="相对于 \`${prev_tag}\`"
else
  range="${SHA}"
  range_desc="本次提交"
fi

# ── 收集提交标题 ──────────────────────────────────────────────────────
commits=""
if git rev-parse --git-dir >/dev/null 2>&1; then
  commits="$(git log --no-merges --pretty=format:'%s' "$range" 2>/dev/null || true)"
fi

# 按 conventional-commit 前缀归类；没有前缀的落到"其它改动"。
features="$(printf '%s\n' "$commits" | grep -E '^feat' | sed 's/^/- /' || true)"
fixes="$(printf '%s\n' "$commits" | grep -E '^fix' | sed 's/^/- /' || true)"
others="$(printf '%s\n' "$commits" | grep -vE '^(feat|fix)' | grep -vE '^$' | sed 's/^/- /' || true)"

# ── 变更规模 ──────────────────────────────────────────────────────────
shortstat=""
if git rev-parse --git-dir >/dev/null 2>&1; then
  shortstat="$(git diff --shortstat "$range" 2>/dev/null || true)"
fi
[ -n "$shortstat" ] || shortstat="（无法统计）"

built_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

# ── 拼装 Markdown ─────────────────────────────────────────────────────
echo "## 本次更新（${range_desc}）"
echo

if [ -n "$features" ]; then
  echo "### 新功能"
  echo
  echo "$features"
  echo
fi

if [ -n "$fixes" ]; then
  echo "### 修复"
  echo
  echo "$fixes"
  echo
fi

if [ -n "$others" ]; then
  echo "### 其它改动"
  echo
  echo "$others"
  echo
fi

if [ -z "$features$fixes$others" ]; then
  echo "本次提交没有独立的变更说明（可能是纯合并提交或版本线升级）。"
  echo
fi

echo "### 变更规模"
echo
echo '```'
echo "$shortstat"
echo '```'
echo

echo "### 部署信息"
echo
echo "| 项 | 值 |"
echo "| --- | --- |"
echo "| Commit | \`${short_sha}\` |"
# 服务器部署脚本会从 Release 正文里正则抓第一个 40 位十六进制串当 commit，
# 所以这里必须显式输出**完整 SHA**；只给短 SHA 会让它记成 unknown。
echo "| 完整 SHA | ${SHA} |"
echo "| 构建号 | ${RUN_NUMBER} |"
echo "| 版本线 | \`${CURRENT_TAG}\` |"
echo "| 滚动标签 | \`${ROLLING_TAG}\` |"
echo "| 构建时间 | ${built_at} |"
echo
echo "> 产物：\`code-analyse-agent.gz\`（后端二进制）、\`web-dist.tar.gz\`（前端静态资源）、\`version.json\`（构建指纹）。"
echo "> 服务器通过 systemd timer 主动拉取对应 Release 完成部署。"
