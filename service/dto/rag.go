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
