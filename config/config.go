package config

import (
	"flag"
	"fmt"
	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
	"os"
	"sync"
	"time"
)

const (
	ServerName     = "agent_code"
	ServerFullName = "edu.agent.code"
)

var (
	etcdKey         string
	etcdEnv         string
	etcdAddr        string
	localConfigPath string
	globalConfigMu  sync.RWMutex
	globalConfig    Config
)

//goland:noinspection SpellCheckingInspection
type Config struct {
	Server         Server         `yaml:"server"`
	SQLite         SQLite         `yaml:"sqlite"`
	Redis          Redis          `yaml:"redis"`
	DeepSeek       DeepSeek       `yaml:"deepseek"`
	OTel           OTel           `yaml:"otel"`
	MCP            MCP            `yaml:"mcpserver"`
	DatabaseReport DatabaseReport `yaml:"database_report"`
	Agents         Agents         `yaml:"agents"`
	ModelPrice     ModelPrice     `yaml:"model_price"`
	WorkSpace      WorkSpace      `yaml:"workspace"`
	RAG            RAG            `yaml:"rag"`
	Skills         Skills         `yaml:"skills"`
	MCPServerSelf  MCPServerSelf  `yaml:"mcp_server_self"`
	Auth           Auth           `yaml:"auth"`
	ContextCompact ContextCompact `yaml:"context_compact"`
}

// ContextCompact 是上下文压缩（compaction）配置。
//
// 压缩由 service/agent/compress 基于 Eino 自带 summarization 中间件实现：
// 每当准备调用模型时统计当前 prompt（历史消息 + 工具 schema）的 token 用量，
// 超过阈值就把较早的对话折叠成一条结构化摘要，从而避免上下文窗口被打满。
type ContextCompact struct {
	// Enabled 为 false 时完全不注册压缩中间件，也不注入/累积跨轮摘要，
	// 行为与改造前完全一致。
	Enabled bool `yaml:"enabled"`
	// WindowTokens 是模型上下文窗口大小（token），用于按比例推导触发阈值。
	WindowTokens int `yaml:"window_tokens"`
	// TriggerRatio 是触发压缩的窗口占用比例，(0,1]，默认 0.6。
	TriggerRatio float64 `yaml:"trigger_ratio"`
	// TriggerTokens 直接指定触发阈值（token）；>0 时优先于 TriggerRatio。
	TriggerTokens int `yaml:"trigger_tokens"`
	// TriggerMessages 是消息条数兜底阈值；<=0 时使用默认值。
	TriggerMessages int `yaml:"trigger_messages"`
	// Model 是生成摘要使用的模型；为空或与 DeepSeek.Model 相同时沿用主链路模型，
	// 配置成别的模型名则用同一个 DeepSeek 端点单独构造摘要模型。
	Model string `yaml:"model"`
	// Instruction 是自定义摘要指令；为空时使用内置中文指令。
	Instruction string `yaml:"instruction"`
	// TranscriptPath 是完整会话记录路径，会写进摘要提醒模型可回读原始上下文。
	TranscriptPath string `yaml:"transcript_path"`
	// KeepRecent 是压缩时原样保留的最近消息条数（不含 system 消息）。
	//
	// 只有更早的历史会被折叠成摘要；<=0 时使用默认值 10。这样"当前正在做的事"
	// 始终以原文出现在模型输入里，不需要经过摘要模型二次加工。
	KeepRecent int `yaml:"keep_recent"`
	// Strategy 选择压缩策略：
	//   - "structured"（默认，空字符串等价于此）：system 消息永不进摘要，
	//     最近 KeepRecent 条原样保留，只有更早的历史会生成结构化证据摘要；
	//   - "legacy"：改造前的行为——整段历史（含 system）交给摘要模型，再用摘要替换全部历史。
	//     保留该取值是为了让 A/B 评测能够原样复现旧行为。
	Strategy string `yaml:"strategy"`
}

type MCPServerSelf struct {
	Enabled  bool   `yaml:"enabled"`
	HttpAddr string `yaml:"http_addr"`
}

type Server struct {
	AppName  string `yaml:"app_name"`
	Version  string `yaml:"version"`
	HTTPAddr string `yaml:"http_addr"`
	LogLevel string `yaml:"log_level"`
}

type SQLite struct {
	Path string `yaml:"path"`
}

// Redis 是登录令牌存储依赖的 Redis 连接配置。
type Redis struct {
	Addr     string `yaml:"addr"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
}

// Auth 是账号密码登录配置。Account.Plan 取值为 common.Plan 的字符串形式。
type Auth struct {
	// TokenTTLHours 是登录令牌有效期（小时），<=0 时按 168 小时（7 天）处理。
	TokenTTLHours int           `yaml:"token_ttl_hours"`
	Accounts      []AuthAccount `yaml:"accounts"`
	// Guest 是游客登录配置：为没有账号的访客提供免密码入口，
	// 身份由 IP + 浏览器指纹派生，用量计入该派生身份的配额。
	Guest Guest `yaml:"guest"`
}

type AuthAccount struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	Plan     string `yaml:"plan"`
}

// Guest 是游客登录配置。
type Guest struct {
	// Enabled 为 true 时才允许游客登录；不配置时默认关闭，避免线上被静默放开。
	Enabled bool `yaml:"enabled"`
	// Plan 是游客身份使用的套餐，取值为 common.Plan 的字符串形式；
	// 留空或取值非法时按 plus 处理。
	Plan string `yaml:"plan"`
	// DailyQuota 覆盖游客身份的单日调用上限；<=0 时沿用套餐自带额度。
	DailyQuota int `yaml:"daily_quota"`
	// TokenTTLHours 是游客令牌有效期（小时）；<=0 时按 24 小时处理，且不超过 auth.token_ttl_hours。
	TokenTTLHours int `yaml:"token_ttl_hours"`
}

type DatabaseReport struct {
	Enable          bool   `yaml:"enable"`
	Driver          string `yaml:"driver"`
	DSN             string `yaml:"dsn"`
	Database        string `yaml:"database"`
	MaxRows         int    `yaml:"max_rows"`
	MaxCellRunes    int    `yaml:"max_cell_runes"`
	QueryTimeoutSec int    `yaml:"query_timeout_sec"`
}

type MCP struct {
	Enable  bool        `yaml:"enable"`
	Servers []MCPServer `yaml:"servers"`
}

type MCPServer struct {
	Name           string            `yaml:"name"`             // Server 名，用于日志和默认工具名前缀。
	Enabled        bool              `yaml:"enabled"`          // 是否启用该 Server；未配置时按启用处理。
	Transport      string            `yaml:"transport"`        // stdio 或 sse streamable。
	URL            string            `yaml:"url"`              // sse transport endpoint。
	Command        string            `yaml:"command"`          // stdio transport 命令。
	Args           []string          `yaml:"args"`             // stdio transport 参数。
	Env            map[string]string `yaml:"env"`              // stdio transport 环境变量增量。
	Headers        map[string]string `yaml:"headers"`          // MCP 请求 headers。
	ToolPrefix     string            `yaml:"tool_prefix"`      // 暴露给模型的工具名前缀；为空默认使用 name。
	Groups         []string          `yaml:"groups"`           // 这些工具加载到哪些agent中，direct、analysis、qa；为空默认 analysis。
	IncludeTools   []string          `yaml:"include_tools"`    // 只加载这些 MCP 原始工具名，空表示加载所有的。
	ExcludeTools   []string          `yaml:"exclude_tools"`    // 排除这些 MCP 原始工具名。
	Required       bool              `yaml:"required"`         // true 表示该 Server 加载失败时服务启动失败。
	InitTimeoutSec int               `yaml:"init_timeout_sec"` // 初始化超时秒数。
}

type RAG struct {
	Enabled            bool      `yaml:"enabled"`
	DocsRoot           string    `yaml:"docs_root"`
	AutoIndexOnStartup bool      `yaml:"auto_index_on_startup"`
	ChunkSize          int       `yaml:"chunk_size"`
	ChunkOverlap       int       `yaml:"chunk_overlap"`
	MaxFileBytes       int       `yaml:"max_file_bytes"`
	MaxContextRunes    int       `yaml:"max_context_runes"`
	TopK               int       `yaml:"top_k"`
	ScoreThreshold     int       `yaml:"score_threshold"` // 混合检索 一般0-0.5  向量检索0-1.0
	Embedding          Embedding `yaml:"embedding"`
	Milvus             Milvus    `yaml:"milvus"`
	Rerank             Rerank    `yaml:"rerank"`
	Rewrite            Rewrite   `yaml:"rewrite"`
}

// Rewrite 中文查询改写：把中文自然语言提问改写为英文检索式，并抽取关键词与子问题。
type Rewrite struct {
	Enabled       bool   `yaml:"enabled"`        // 是否启用查询改写，默认关闭。
	Provider      string `yaml:"provider"`       // 供应方，目前支持 "deepseek"。
	BaseURL       string `yaml:"base_url"`       // 自定义端点。
	APIKey        string `yaml:"api_key"`        // 鉴权密钥。
	Model         string `yaml:"model"`          // 模型名。
	TimeoutSec    int    `yaml:"timeout_sec"`    // 单次改写超时秒数，默认 20。
	MaxSubqueries int    `yaml:"max_subqueries"` // 最多拆分的子问题数，默认 3。
}

type Rerank struct {
	Enabled    bool   `yaml:"enabled"`     // 是否启用 Rerank。
	Provider   string `yaml:"provider"`    // 供应方："dashscope" / "llamacpp"。
	BaseURL    string `yaml:"base_url"`    // 自定义端点；llamacpp 填根地址。
	APIKey     string `yaml:"api_key"`     // 鉴权密钥，本地配置不应提交真实值。
	Model      string `yaml:"model"`       // 模型名："qwen3-reranker-0.6b" "。
	TopN       int    `yaml:"top_n"`       // 精排后保留的 chunk 数，默认 5。
	TimeoutSec int    `yaml:"timeout_sec"` // 单次 Rerank 超时秒数，默认由 RAG 服务兜底。
}

type Embedding struct {
	BaseUrl    string `yaml:"base_url"`
	APIKey     string `yaml:"api_key"`
	Model      string `yaml:"model"`
	Dimensions int    `yaml:"dimensions"`
	TimeoutSec int    `yaml:"timeout_sec"`
}

type Milvus struct {
	Address         string `yaml:"address"`
	UserName        string `yaml:"username"`
	Password        string `yaml:"password"`
	Collection      string `yaml:"collection"`
	Partition       string `yaml:"partition"`
	MetricsType     string `yaml:"metric_type"`
	HybridEnabled   bool   `yaml:"hybrid_enabled"`
	DropBeforeIndex bool   `yaml:"drop_before_index"`
	TimeoutSec      int    `yaml:"timeout_sec"`
	// DenseTopK / SparseTopK 是 hybrid 模式下两路单路检索各取多少条候选；
	// CandidateK 是 RRF 融合后保留、再交给重排（或直接返回）的候选池大小。
	// 这三个值把"候选池"与"最终返回条数"解耦：单路 TopK 决定各路召回面，
	// CandidateK 决定融合后的候选池，最终返回条数由调用方传入的 topK 决定。
	// 三者任意一个 <= 0 时回落到 TopK，保持与旧配置完全一致的行为。
	DenseTopK  int `yaml:"dense_top_k"`
	SparseTopK int `yaml:"sparse_top_k"`
	CandidateK int `yaml:"candidate_k"`
}

type Skills struct {
	Enabled     bool     `yaml:"enabled"`
	Directories []string `yaml:"directories"`
	ToolName    string   `yaml:"tool_name"`
}

type DeepSeek struct {
	APIKey  string `yaml:"api_key"`
	BaseURL string `yaml:"base_url"`
	Model   string `yaml:"model"`
}

type OTel struct {
	Endpoint   string  `yaml:"endpoint"`
	SampleRate float64 `yaml:"sample_rate"`
	StdOut     bool    `yaml:"std_out"`
}

type WorkSpace struct {
	Root string `yaml:"root"`
	// ReposDir 是 repo_fetch 拉取远端仓库的存放目录，为空时默认 <root>/repos。
	ReposDir string `yaml:"repos_dir"`
	// GitCloneTimeoutSec 是单次 git clone/fetch 的超时秒数，为空或 <=0 时默认 300 秒。
	GitCloneTimeoutSec int `yaml:"git_clone_timeout_sec"`
	// MaxRepoMB 是单个仓库的体积上限（MB），超限会清理并报错，为空或 <=0 时默认 2048。
	MaxRepoMB     int  `yaml:"max_repo_mb"`
	EnableEscaped bool `yaml:"enable_escaped"`
	CommonDetect  bool `yaml:"common_detect"`
	// GitMirrorPrefix 是国外仓库（目前只覆盖 github.com）的拉取加速前缀。
	// 例：填 https://ghfast.top/ 后，https://github.com/o/r 实际会从
	// https://ghfast.top/https://github.com/o/r 拉取，仓库身份仍按原始地址计算。
	// 留空表示使用内置默认值（https://ghfast.top/）；显式填 "-" 表示关闭镜像、直连。
	GitMirrorPrefix string `yaml:"git_mirror_prefix"`
}

type ModelPrice struct {
	FallbackModel string                   `yaml:"fallback_model"`
	PriceCNY      map[string]ModelPriceCNY `yaml:"price_cny"`
}

type ModelPriceCNY struct {
	Prompt     float64 `yaml:"prompt"`     // 输入单价（缓存未命中）
	CacheHit   float64 `yaml:"cache_hit"`  // 缓存命中单价；必须显式配置为正数，否则 cost.Track 直接报错
	Completion float64 `yaml:"completion"` // 输出单价
}

type Agents struct {
	EnableChinese bool               `yaml:"enable_cn"`
	MaxIterations AgentMaxIterations `yaml:"max_iterations"`
}

type AgentMaxIterations struct {
	Compose              int `yaml:"compose"`
	ProjectQA            int `yaml:"project_qa"`
	RepoAnalyzer         int `yaml:"repo_analyzer"`
	RepoAnalyzerSubAgent int `yaml:"repo_analyzer_sub_agent"`
	DBReport             int `yaml:"db_report"`
}

func init() {
	flag.StringVar(&localConfigPath, "c", ServerName+"_local.yml", "path to local config file")
	flag.StringVar(&etcdAddr, "r", os.Getenv("ETCD_ADDR"), "ETCD server address")
	flag.StringVar(&etcdEnv, "e", "env", "ETCD environment")
}

func InitConfig() *Config {
	vipConf := viper.New()
	vipConf.SetConfigType("yaml")
	flag.Parse()

	etcdKey = fmt.Sprintf("/config/%s/%s/system", ServerFullName, etcdEnv)
	if etcdAddr != "" {
		conf, err := getFromRemoteAndWatchUpdate(vipConf)
		if err != nil {
			panic(err)
		}
		globalConfig = *conf
		return conf
	}
	conf, err := getFromLocal()
	if err != nil {
		panic(err)
	}
	if conf.DeepSeek.APIKey == "" {
		conf.DeepSeek.APIKey = os.Getenv("DEEPSEEK_API_KEY")
	}
	globalConfig = *conf
	return conf
}

func getFromLocal() (*Config, error) {
	data, err := os.ReadFile(localConfigPath)
	if err != nil {
		return nil, err
	}
	var config Config
	err = yaml.Unmarshal(data, &config)
	if err != nil {
		return nil, err
	}
	return &config, nil
}

func getFromRemoteAndWatchUpdate(v *viper.Viper) (*Config, error) {
	if err := v.AddRemoteProvider("etcd3", etcdAddr, etcdKey); err != nil {
		return nil, err
	}
	if err := v.ReadRemoteConfig(); err != nil {
		return nil, err
	}
	config := Config{}
	if err := unmarshalViperConfig(v, &config); err != nil {
		return nil, err
	}
	go func() {
		for {
			time.Sleep(time.Minute)
			if err := v.WatchRemoteConfig(); err == nil {
				globalConfigMu.Lock()
				_ = unmarshalViperConfig(v, &globalConfig)
				globalConfigMu.Unlock()
			}
		}
	}()

	return &config, nil
}

func unmarshalViperConfig(v *viper.Viper, config *Config) error {
	return v.Unmarshal(config, func(config *mapstructure.DecoderConfig) {
		config.TagName = "yaml"
	})
}

func GetLatestConfig() Config {
	globalConfigMu.RLock()
	defer globalConfigMu.RUnlock()
	return globalConfig
}
