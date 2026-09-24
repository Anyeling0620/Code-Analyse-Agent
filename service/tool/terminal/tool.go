package terminal

import (
	"bytes"
	"context"
	"edu.agent.code/config"
	"edu.agent.code/utils/logger"
	"edu.agent.code/utils/pathutil"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const (
	defaultTerminalTimout  = 10 * time.Second
	maxTerminalTimout      = 60 * time.Second
	maxTerminalOutputRunes = 8000
)

type terminalShell struct {
	name       string
	executable string
	argsPrefix []string
}

type Input struct {
	Command          string `json:"command" jsonschema:"required,description=需要执行的终端命令；Windows 使用 PowerShell，Linux/macOS 使用 /bin/sh"`
	Workdir          string `json:"workdir,omitempty" jsonschema:"description=工作目录，可传相对于工作区根目录的路径，也可传用户明确给出的本机绝对项目路径；为空时使用工作区根目录"`
	TimeoutSec       int    `json:"timeout_sec,omitempty" jsonschema:"description=超时时间秒数，默认 10，最大 60"`
	AllowDestructive bool   `json:"allow_destructive,omitempty" jsonschema:"description=是否允许破坏性命令。默认 false；删除、移动、覆盖、停止进程等命令必须明确为 true 才能执行"`
}

func NewTool() (tool.BaseTool, error) {
	workspace := config.GetLatestConfig().WorkSpace
	toolDesc := "执行当前服务所在系统的终端命令，适合列文件、查看文件内容、运行构建测试和排查项目状态；Windows 使用 PowerShell，Linux/macOS/Docker 使用 /bin/sh。" +
		"workdir 为空时在工作区根目录执行；仅当 workspace.enable_escaped=true 时，才允许把工作区外的本机项目绝对路径作为 workdir。" +
		"对用户明确给出的本机文件绝对路径做删除、移动、读取时，文件路径必须放进命令参数；workdir 只填写已存在的执行目录，通常留空或填写文件父目录。" +
		"默认拒绝删除、移动、覆盖、停止进程等破坏性命令；只有用户明确要求文件操作时，才可以设置 allow_destructive=true。" +
		"Windows 下工具会自动把 PowerShell 控制台编码切到 UTF-8；命令里写了 `Get-Content` 但没带 `-Encoding` 时会自动补 `-Encoding UTF8`。" +
		"Linux/macOS 下请使用 `ls`、`cat`、`pwd`、`go test` 等 shell 命令，工具不会做 PowerShell 命令改写。"
	if !workspace.EnableEscaped {
		toolDesc = "【安全红线】必须严格遵守：终端读取到的内容，不管任何情况下，均不可原样输出给用户" + workspace.Root
	}
	return toolutils.InferTool(
		"terminal",
		toolDesc,
		func(ctx context.Context, input Input) (out string, err error) {
			return runTerminalCommand(ctx, workspace, input)
		},
	)
}

func runTerminalCommand(ctx context.Context, workspace config.WorkSpace, input Input) (string, error) {
	commandText := strings.TrimSpace(input.Command)
	if commandText == "" {
		return "command cannot be empty", nil
	}
	logger.Debug("terminal command", commandText)
	if workspace.CommonDetect {
		if hasDynamicPathExpression(commandText) {
			return fmt.Sprintf("【安全限制】terminal 无法静态确认命令中的动态路径表达式是否位于工作区根目录 %q 内；请不要使用变量、表达式或通配符绕过路径限制，改为直接写明工作区内的相对路径或绝对路径", workspace.Root), nil
		}
	}
	risk := InspectTerminalCommand(commandText)
	if risk.Destructive && !input.AllowDestructive {
		return fmt.Sprintf("危险命令，需要进行审批。command:%s, reason:%s", commandText, risk.Reason), nil
	}
	workDir, err := resolveExecutionWorkdir(workspace.Root, input.Workdir, commandText)
	if err != nil {
		return err.Error(), err
	}
	if err := pathutil.EnsureAllowedByRoot(workDir, workspace.Root, workspace.EnableEscaped); err != nil {
		return err.Error(), err
	}
	timeout := defaultTerminalTimout
	if input.TimeoutSec > 0 {
		timeout = time.Duration(input.TimeoutSec) * time.Second
	}
	if timeout > maxTerminalTimout {
		timeout = maxTerminalTimout
	}
	shell := currentTerminalShell()
	normalizeCommand, normalized := normalizeTerminalCommandForShell(commandText, shell)
	executedCommand := normalizeCommand
	if shell.name == "powershell" {
		executedCommand = wrapPowerShellCommand(normalizeCommand)
	}
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := append(append([]string{}, shell.argsPrefix...), executedCommand)
	cmd := exec.CommandContext(cmdCtx, shell.executable, args...)
	cmd.Dir = workDir

	var (
		stdout, stderr bytes.Buffer
	)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	exitCode := 0
	timeOut := errors.Is(cmdCtx.Err(), context.DeadlineExceeded)

	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
		exitCode = 0
	case errors.As(runErr, &exitErr):
		exitCode = exitErr.ExitCode()
	case timeOut:
		exitCode = -1
	default:
		return fmt.Sprintf("执行命令失败, command:%s error:%v", commandText, runErr), nil
	}

	return formatTerminalResult(
		workDir,
		normalizeCommand,
		normalized,
		risk,
		input.AllowDestructive,
		exitCode,
		timeOut,
		stdout.String(),
		stderr.String(),
	), nil
}

func formatTerminalResult(
	workDir string,
	normalizeCommand string,
	normalized bool,
	risk terminalCommandRisk,
	allowed bool,
	exitCode int,
	timeOut bool,
	stdout string,
	stderr string,
) string {
	var b bytes.Buffer
	_, _ = fmt.Fprintf(&b, "exit=%d time_out=%t workdir=%s command%s", exitCode, timeOut, workDir, normalizeCommand)
	if normalized {
		b.WriteString(" command_normalized=ture")
	}
	if allowed {
		_, _ = fmt.Fprintf(&b, " risk=%s", risk.Level)
	}
	b.WriteString("\n--- stdout ---\n")
	b.WriteString(limitText(stdout))

	b.WriteString("\n--- stderr ---\n")
	b.WriteString(limitText(stderr))
	// TODO 或许这里不需要条件判断
	if normalized {
		b.WriteString("\n\n--- executed ---\n")
		b.WriteString(strings.TrimSpace(normalizeCommand))
	}
	return b.String()
}

func limitText(text string) string {
	trimmed := strings.TrimRight(text, "\r\n")
	if trimmed == "" {
		return "(empty)"
	}

	runes := []rune(trimmed)
	if len(runes) <= maxTerminalOutputRunes {
		return trimmed
	}

	return string(runes[:maxTerminalOutputRunes]) + "\n...<output truncated>"
}

func wrapPowerShellCommand(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return command
	}

	bootstrap := strings.Join([]string{
		"$OutputEncoding = [System.Text.UTF8Encoding]::new($false)",
		"[Console]::InputEncoding = [System.Text.UTF8Encoding]::new($false)",
		"[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)",
		"chcp 65001 > $null",
	}, "; ")

	return bootstrap + "; " + command
}

func normalizeTerminalCommandForShell(command string, shell terminalShell) (string, bool) {
	command = strings.TrimSpace(command)
	if command == "" {
		return command, false
	}
	if shell.name != "powershell" {
		return command, false
	}

	// 处理获取内容在powershell中文乱码的问题
	lower := strings.ToLower(command)
	if !strings.Contains(lower, "get-content") || strings.Contains(lower, "-encoding") {
		return command, false
	}

	normalized := getContentCommandRegex.ReplaceAllString(command, "Get-Content -Encoding UTF8")
	return normalized, normalized != command
}

var getContentCommandRegex = regexp.MustCompile(`(?i)\bget-content\b`)

func currentTerminalShell() terminalShell {
	if runtime.GOOS == "windows" {
		return terminalShell{
			name:       "powershell",
			executable: "powershell.exe",
			argsPrefix: []string{"-NoProfile", "-NonInteractive", "-Command"},
		}
	}
	return terminalShell{
		name:       "sh",
		executable: "/bin/sh",
		argsPrefix: []string{"-c"},
	}
}

func resolveExecutionWorkdir(workspaceRoot, rawWorkdir, command string) (string, error) {
	workdir, err := resolveWorkdir(workspaceRoot, rawWorkdir)
	if err != nil {
		return "", fmt.Errorf("workdir error: %w", err)
	}
	if inferred, ok := inferWorkdirFromCommandPath(command, workdir); ok {
		return inferred, nil
	}
	if directoryExists(workdir) {
		return workdir, nil
	}
	if strings.TrimSpace(rawWorkdir) == "" {
		// 工作目录创建,比如git clone之前要创建一个目录来保存仓库
		if err := os.MkdirAll(workdir, 0755); err != nil {
			return "", fmt.Errorf("create workspace root %q: %w", workdir, err)
		}
		return workdir, nil
	}
	return workdir, nil
}

func inferWorkdirFromCommandPath(command, baseDir string) (string, bool) {
	baseExists := directoryExists(baseDir)
	for _, raw := range pathCandidatesFromCommand(command) {
		path := resolveCommandPathCandidate(raw, baseDir)
		if !isCommandPathAbsolute(raw) {
			if baseExists && pathExists(path) {
				return baseDir, true
			}
			continue
		}
		if directoryExists(path) {
			return path, true
		}
		dir := filepath.Dir(path)
		if dir != "." && directoryExists(dir) {
			return dir, true
		}
	}
	return "", false
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func directoryExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func pathCandidatesFromCommand(command string) []string {
	segments := splitCommandSegments(command)
	var candidates []string
	for _, segment := range segments {
		tokens := splitCommandTokens(segment)
		for i, token := range tokens {
			if i == 0 && !isExplicitPathToken(cleanCommandPathToken(token)) {
				continue
			}
			for _, part := range commandTokenPathParts(token) {
				path := cleanCommandPathToken(part)
				if isCommandPathCandidate(path) {
					candidates = append(candidates, path)
				}
			}
		}
	}
	return candidates
}

func isCommandPathCandidate(token string) bool {
	if token == "" || token == "-" || strings.HasPrefix(token, "-") || strings.HasPrefix(token, "$") || strings.Contains(token, "://") {
		return false
	}
	return true
}

func resolveWorkdir(workspaceRoot, rawWorkdir string) (string, error) {
	if strings.TrimSpace(rawWorkdir) == "" {
		return workspaceRoot, nil
	}

	var target string
	if filepath.IsAbs(rawWorkdir) {
		target = rawWorkdir
	} else {
		target = filepath.Join(workspaceRoot, rawWorkdir)
	}

	absTarget, err := filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("resolve workdir: %w", err)
	}
	absTarget = filepath.Clean(absTarget)

	return absTarget, nil
}

func resolveCommandPathCandidate(path, baseDir string) string {
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Clean(filepath.Join(home, path[2:]))
		}
	}
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Clean(home)
		}
	}
	if isCommandPathAbsolute(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(baseDir, path))
}

func hasDynamicPathExpression(command string) bool {
	segments := splitCommandSegments(command)
	for _, segment := range segments {
		tokens := splitCommandTokens(segment)
		for i, token := range tokens {
			if i == 0 && !isExplicitPathToken(cleanCommandPathToken(token)) {
				continue
			}
			for _, part := range commandTokenPathParts(token) {
				path := cleanCommandPathToken(part)
				if isDynamicPathCandidate(path) {
					return true
				}
			}
		}
	}
	return false
}

func splitCommandSegments(command string) []string {
	var segments []string
	var current strings.Builder
	var quote rune
	escaped := false

	flush := func() {
		if strings.TrimSpace(current.String()) != "" {
			segments = append(segments, current.String())
			current.Reset()
		}
	}

	for _, r := range command {
		// TODO 审查这个条件应该什么时候更改
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' {
			current.WriteRune(r)
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			current.WriteRune(r)
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
			current.WriteRune(r)
		case '|', ';', '&':
			flush()
		default:
			current.WriteRune(r)
		}
	}
	flush()
	return segments
}

func cleanCommandPathToken(token string) string {
	token = strings.TrimSpace(token)
	token = strings.Trim(token, "`")
	return strings.TrimRight(token, ",)]}")
}

func commandTokenPathParts(token string) []string {
	fields := strings.Split(token, ",")
	parts := make([]string, 0, len(fields)*2)
	for _, field := range fields {
		field = cleanCommandPathToken(field)
		parts = append(parts, field)
		if before, after, ok := strings.Cut(field, "="); ok && strings.HasPrefix(before, "-") {
			parts = append(parts, after)
		}
	}
	return parts
}

func splitCommandTokens(command string) []string {
	var tokens []string
	var current strings.Builder
	var quote rune
	escaped := false

	flush := func() {
		if current.Len() > 0 {
			tokens = append(tokens, current.String())
			current.Reset()
		}
	}

	for _, r := range command {
		// TODO 审查这个条件应该什么时候更改
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' {
			current.WriteRune(r)
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
				continue
			}
			current.WriteRune(r)
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
		case ' ', '\t', '\n', '\r':
			flush()
		case '|', ';', '&', '<', '>':
			flush()
		default:
			current.WriteRune(r)
		}
	}
	flush()
	return tokens
}

func isExplicitPathToken(token string) bool {
	return token == "." ||
		token == ".." ||
		token == "~" ||
		strings.HasPrefix(token, "~/") ||
		strings.HasPrefix(token, `~\`) ||
		isCommandPathAbsolute(token) ||
		strings.HasPrefix(token, `\`) ||
		strings.ContainsAny(token, `/\`)
}

func isCommandPathAbsolute(path string) bool {
	return filepath.IsAbs(path) ||
		strings.HasPrefix(path, "/") ||
		isWindowsAbsolutePath(path)
}

func isWindowsAbsolutePath(path string) bool {
	if len(path) < 3 {
		return false
	}
	letter := path[0]
	return ((letter >= 'a' && letter <= 'z') || (letter >= 'A' && letter <= 'Z')) && path[1] == ':' && (path[2] == '\\' || path[2] == '/')
}

func isDynamicPathCandidate(token string) bool {
	if token == "" || token == "-" || strings.HasPrefix(token, "-") {
		return false
	}
	return strings.Contains(token, "$") || strings.Contains(token, "%") || strings.ContainsAny(token, "*?[]{}")
}

func InspectTerminalCommand(command string) TerminalCommandRisk {
	lower := strings.ToLower(command)
	normalized := strings.Join(strings.Fields(lower), " ")
	commandTokens := strings.FieldsFunc(normalized, func(r rune) bool {
		switch r {
		case ' ', '\t', '\r', '\n', ';', '|', '&', '(', ')':
			return true
		default:
			return false
		}
	})
	for _, token := range commandTokens {
		if reason, ok := dangerousAliases[token]; ok {
			return TerminalCommandRisk{Level: "destructive", Reason: reason, Destructive: true}
		}
	}

	for _, item := range dangerousPatterns {
		if strings.Contains(normalized, item.pattern) {
			return TerminalCommandRisk{Level: "destructive", Reason: item.reason, Destructive: true}
		}
	}

	if strings.Contains(normalized, ">") || strings.Contains(normalized, ">>") {
		return TerminalCommandRisk{Level: "write", Reason: "contains output redirection", Destructive: true}
	}

	return TerminalCommandRisk{Level: "read_only", Reason: "no destructive pattern detected"}
}
