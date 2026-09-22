package do

import "time"

type Profile struct {
	UserID           string
	AuthSubject      string
	UserType         string
	SkillLevel       string
	GoalType         string
	PurchasedCourses []string
	CurrentTopic     string
	CurrentStage     string
	UpdatedAt        time.Time
}

type ProfileUpdate struct {
	UserID           string
	SkillLevel       string
	GoalType         string
	PurchasedCourses []string
	CurrentTopic     string
	CurrentStage     string
}
