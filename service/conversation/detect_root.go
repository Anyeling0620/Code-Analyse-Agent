package conversation

import (
	"path/filepath"
	"regexp"
	"strings"

	"edu.agent.code/service/dto"
	"edu.agent.code/utils/pathutil"
)

var projectPathCandidatePattern = regexp.MustCompile(`(?:'([^']+)'|"([^"]+)"|([^\s，。；;]+))`)

func updateProjectContextFromMessage(session *dto.SessionContext, message string) {
	if session == nil {
		return
	}
	root := detectProjectRoot(message)
	if root == "" {
		return
	}
	session.CurrentProjectRoot = root
	session.CurrentProjectName = filepath.Base(root)
}

func detectProjectRoot(message string) string {
	for _, match := range projectPathCandidatePattern.FindAllStringSubmatch(message, -1) {
		// match[0] 是完整匹配文本，match[1:] 才是单引号、双引号或裸路径分支捕获到的候选路径。
		for _, raw := range match[1:] {
			candidate := cleanProjectPathCandidate(raw)
			if candidate == "" || !filepath.IsAbs(candidate) {
				continue
			}
			root, err := pathutil.NormalizeExistingRoot(candidate)
			if err == nil {
				return root
			}
		}
	}
	return ""
}

func cleanProjectPathCandidate(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.Trim(raw, `"'`)
	raw = strings.TrimRight(raw, "，。；;,.)]}>\r\n\t ")
	if raw == "" {
		return ""
	}
	return filepath.Clean(raw)
}
