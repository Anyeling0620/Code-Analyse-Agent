// Package codeindex 维护项目级"结构化索引"：符号表 + 依赖关系图。
//
// 它与 RAG（向量检索）相互独立：即使 rag.enabled=false，本索引仍能构建与查询；
// 存储在自己的 SQLite 文件里（默认 data/code_index.db），删掉文件即可整体重建。
// 结构化索引回答的是确定性问题——符号在哪、签名是什么、谁依赖谁，
// 不适合用向量召回近似。
package codeindex

// 符号类型。extractor 只输出这些值，查询侧不需要再做映射。
const (
	KindFunc      = "func"
	KindMethod    = "method"
	KindStruct    = "struct"
	KindInterface = "interface"
	KindClass     = "class"
	KindTrait     = "trait"
	KindType      = "type"
	KindEnum      = "enum"
	KindConst     = "const"
	KindVar       = "var"
	KindModule    = "module"
)

// 边类型。import 是文件级依赖，其余是类型级关系。
const (
	EdgeImport     = "import"
	EdgeExtends    = "extends"
	EdgeImplements = "implements"
	EdgeEmbeds     = "embeds"
)

// 置信度。启发式解析出来的边必须显式标注，展示时不能当成事实。
const (
	ConfidenceExact     = "exact"
	ConfidenceHeuristic = "heuristic"
)

// Symbol 是一个可检索的声明。Signature 是压平后的单行声明头（不含函数体），
// 由 extractor 截断到合理长度；Parent 是所属类型/类，顶层符号为空。
type Symbol struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Parent    string `json:"parent,omitempty"`
	Signature string `json:"signature,omitempty"`
	LineStart int    `json:"line_start"`
	LineEnd   int    `json:"line_end"`
}

// Edge 是一条有向关系。FromSymbol 为空表示文件级依赖（import）：
// 起点是文件本身，ToRaw 是原始 import 文本（由查询层再解析成本地文件）。
// FromSymbol 非空时，起点是某个类型，ToRaw 是父类型/接口/被嵌入类型的原始名。
type Edge struct {
	FromSymbol string `json:"from_symbol,omitempty"`
	ToRaw      string `json:"to_raw"`
	Kind       string `json:"kind"`
	Confidence string `json:"confidence"`
	Line       int    `json:"line,omitempty"`
}

// Facts 是单个文件的抽取结果。
type Facts struct {
	Language string
	Symbols  []Symbol
	Edges    []Edge
}

// Ref 描述一次索引请求。ProjectID 为空时由调用方用 projectid 派生。
type Ref struct {
	ProjectID string
	Root      string
	Commit    string
}
