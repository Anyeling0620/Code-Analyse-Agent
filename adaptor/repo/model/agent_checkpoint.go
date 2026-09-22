package model

import "time"

// AgentCheckpoint 保存 Eino Runner interrupt/resume 需要的 checkpoint 数据。
type AgentCheckpoint struct {
	CheckpointID string    `gorm:"primaryKey;size:256"` // conversation 每轮生成的 checkpoint id。
	Data         []byte    `gorm:"type:blob"`           // Eino 序列化后的 Runner 状态，业务层不解析。
	UpdatedAt    time.Time // checkpoint 最近写入时间。
}

// TableName 返回 checkpoint 表名。
func (AgentCheckpoint) TableName() string { return "agent_checkpoints" }
