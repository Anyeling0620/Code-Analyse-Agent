package conversation

import "testing"

// TestLooksLikeToolError 用**各工具真实的返回文本**锁定判定口径。
//
// 这些用例之所以值得钉住：本项目的工具普遍把错误当正常结果返回
// （(string, error) 里的 string），eino 层看不到 Go error，
// tool_errors 指标完全依赖这个启发式。改动这里的规则等于改报表口径。
func TestLooksLikeToolError(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		// —— 真实错误文本（取自各工具源码的返回值）——
		{"db_read_query 拒绝非只读 SQL", "query contains non-read-only keyword, denied", true},
		{"db_describe_table 表不存在", "table edu_report.no_such_table does not exist", true},
		{"MySQL 驱动错误", "Error 1146 (42S02): Table 'edu_report.x' doesn't exist", true},
		{"http_request 方法不允许", "http method TRACE not allowed", true},
		{"http_request 请求失败", "http request error: dial tcp: i/o timeout", true},
		{"read_files 参数非法", "invalid read_files argument: root 必须是项目根路径", true},
		{"project_search 空查询", "query cannot be empty", true},
		{"terminal 执行失败", "执行命令失败, command:ls -zz error:exit status 2", true},
		{"terminal 路径安全限制", "【安全限制】terminal 无法静态确认命令中的动态路径表达式是否位于工作区根目录内", true},
		{"中文超时", "检索超时，请稍后重试", true},

		// —— 正常结果不应被误判 ——
		{"空结果", "", false},
		{"空表", "(no rows)", false},
		{"普通 Markdown 表格", "| 课程 | 选课人数 |\n| --- | --- |\n| 编译原理 | 12 |", false},
		{"代码检索结果（error 出现在第二行）", "func main() {\n\treturn errors.New(\"boom\")\n}", false},
		{"工具执行无输出文案", "read_files 工具已执行， 无输出", false},
		{"长结果（首行超长，即使含 error 也不算）", "| " + longRunes(200) + " error |\n| --- |", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := looksLikeToolError(tc.content); got != tc.want {
				t.Fatalf("looksLikeToolError(%q) = %v, want %v", tc.content, got, tc.want)
			}
		})
	}
}

// TestLooksLikeToolErrorKnownFalsePositive 显式记录启发式的已知误判：
// 如果**首行很短且自带 error 字样**的正常检索结果，会被算成工具失败。
//
// 保留这个用例是为了让这个已知偏差可见——若将来要收紧规则，
// 必须连同这里一起改，而不是让报表口径悄悄变化。
func TestLooksLikeToolErrorKnownFalsePositive(t *testing.T) {
	got := looksLikeToolError("return errors.New(\"boom\")")
	if !got {
		t.Fatalf("已知误判消失了，需要重新评估 tool_errors 口径")
	}
}

func longRunes(n int) string {
	out := make([]rune, n)
	for i := range out {
		out[i] = 'x'
	}
	return string(out)
}
