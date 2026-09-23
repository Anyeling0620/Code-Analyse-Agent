package do

import "time"

type Profile struct {
	UserID           string    `json:"user_id"`
	AuthSubject      string    `json:"auth_subject"`
	UserType         string    `json:"user_type"`
	SkillLevel       string    `json:"skill_level"`
	GoalType         string    `json:"goal_type"`
	PurchasedCourses []string  `json:"purchased_courses"`
	CurrentTopic     string    `json:"current_topic"`
	CurrentStage     string    `json:"current_stage"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type ProfileUpdate struct {
	UserID           string
	SkillLevel       string
	GoalType         string
	PurchasedCourses []string
	CurrentTopic     string
	CurrentStage     string
}
