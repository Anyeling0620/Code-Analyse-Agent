package dto

type RetrieverReq struct {
	Query string `json:"query"`
	TopK  int    `json:"top_k"`
}

type RetrieverChunkDto struct {
	ID         string  `json:"id"`
	Content    string  `json:"content"`
	Header     string  `json:"header"`
	ChunkIndex int     `json:"chunk_index"`
	ChunkSize  int     `json:"chunk_size"`
	FileSize   int     `json:"file_size"`
	Score      float64 `json:"score"`
}

// ProjectIndexStatusReq 查询某个项目语义索引的构建状态。
// project_id 与 root 二选一：project_id 直接命中；只给 root 时由服务端派生 project_id。
type ProjectIndexStatusReq struct {
	ProjectID string `json:"project_id" form:"project_id"`
	Root      string `json:"root" form:"root"`
}

type ProjectIndexStatusDto struct {
	ProjectID  string `json:"project_id"`
	Status     string `json:"status"`
	Commit     string `json:"commit"`
	ChunkCount int    `json:"chunk_count"`
	IndexedAt  string `json:"indexed_at"`
	LastError  string `json:"last_error"`
}
