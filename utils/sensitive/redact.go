package sensitive

import (
	"regexp"
	"strings"
)

var sensitiveLineRegex = regexp.MustCompile(`(?i)^\s*([A-Z0-9_\-.]*(secret|token|password|passwd|pwd|api[_-]?key|private[_-]?key|credential|access[_-]?key)[A-Z0-9_\-.]*\s*[:=]\s*)(.+)$`)

func RedActText(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if sensitiveLineRegex.MatchString(line) {
			lines[i] = sensitiveLineRegex.ReplaceAllString(line, "$1<REDACTED>")
		}
	}
	return strings.Join(lines, "\n")
}
