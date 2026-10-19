package do

import "time"

type Profile struct {
	UserID       string    `json:"user_id"`
	AuthSubject  string    `json:"auth_subject"`
	UserType     string    `json:"user_type"`
	SkillLevel   string    `json:"skill_level"`
	GoalType     string    `json:"goal_type"`
	Description  string    `json:"description"`
	CurrentTopic string    `json:"current_topic"`
	CurrentStage string    `json:"current_stage"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// 画像修订来源。写快照时记录，历史接口据此区分「手动保存」与「对话自动同步」。
const (
	ProfileSourceAPI    = "api"
	ProfileSourceChat   = "chat"
	ProfileSourceLegacy = "legacy"
)

// ProfileRevision 是画像的一次历史快照。profiles 表只保存当前值，
// 每次实际变更在同一事务里追加一条本记录，用于回溯“什么时候改成了什么”。
type ProfileRevision struct {
	ID           uint64    `json:"id"`
	UserID       string    `json:"user_id"`
	UserType     string    `json:"user_type"`
	SkillLevel   string    `json:"skill_level"`
	GoalType     string    `json:"goal_type"`
	Description  string    `json:"description"`
	CurrentTopic string    `json:"current_topic"`
	CurrentStage string    `json:"current_stage"`
	Source       string    `json:"source"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type ProfileUpdate struct {
	UserID       string
	SkillLevel   string
	GoalType     string
	Description  string
	CurrentTopic string
	CurrentStage string
}
