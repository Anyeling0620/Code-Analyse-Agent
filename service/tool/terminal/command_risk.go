package terminal

// 危险命令配置
var (
	dangerousAliases = map[string]string{
		"rm":    "contains rm",
		"del":   "contains del",
		"erase": "contains erase",
		"rd":    "contains rd",
		"mv":    "contains mv",
		"iex":   "contains iex",
	}
	dangerousPatterns = []struct {
		pattern string
		reason  string
	}{
		{`remove-item`, "contains Remove-Item"},
		{`rmdir`, "contains rmdir"},
		{`move-item`, "contains Move-Item"},
		{`set-content`, "contains Set-Content"},
		{`out-file`, "contains Out-File"},
		{`new-item`, "contains New-Item"},
		{`stop-process`, "contains Stop-Process"},
		{`taskkill`, "contains taskkill"},
		{`set-executionpolicy`, "contains Set-ExecutionPolicy"},
		{`invoke-expression`, "contains Invoke-Expression"},
	}
)

// TerminalCommandRisk 是命令风险检查结果。
type TerminalCommandRisk struct {
	Level       string // read_only/write/destructive，用于审批弹窗和工具结果标注。
	Reason      string // 命中的风险规则描述。
	Destructive bool   // true 表示必须经过 approval middleware 或 allow_destructive 才能执行。
}

type terminalCommandRisk = TerminalCommandRisk
