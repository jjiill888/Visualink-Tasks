package model

import "time"

type User struct {
	ID        int64
	Username  string
	Email     string
	Password  string
	Role      string // pm | dev | admin
	CreatedAt time.Time
}

type Group struct {
	ID          int64
	Title       string
	Description string
	CreatedBy   int64
	CreatedAt   time.Time
	// Joined
	CreatorName  string
	FeatureCount int
}

type Feature struct {
	ID          int64
	GroupID     *int64
	Title       string
	Description string
	Priority    string // urgent | high | medium | low
	Status      string // pending | in_progress | done
	CreatedBy   int64
	AssignedTo  *int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
	// Joined
	CreatorName string
	CreatorRole string
	GroupTitle  string
}

func (f Feature) CreatorRoleLabel() string {
	switch f.CreatorRole {
	case "pm":
		return "产品经理"
	case "dev":
		return "开发工程师"
	case "admin":
		return "管理员"
	}
	return f.CreatorRole
}

func (f Feature) PriorityLabel() string {
	switch f.Priority {
	case "urgent":
		return "紧急"
	case "high":
		return "高"
	case "medium":
		return "中"
	case "low":
		return "低"
	}
	return f.Priority
}

func (f Feature) StatusLabel() string {
	switch f.Status {
	case "pending":
		return "待处理"
	case "in_progress":
		return "进行中"
	case "done":
		return "已完成"
	case "rejected":
		return "已驳回"
	}
	return f.Status
}

func (f Feature) StatusBadgeClass() string {
	switch f.Status {
	case "pending":
		return "badge-pending"
	case "in_progress":
		return "badge-progress"
	case "done":
		return "badge-done"
	case "rejected":
		return "badge-rejected"
	}
	return ""
}

func (f Feature) PriorityDotClass() string {
	switch f.Priority {
	case "urgent":
		return "dot-urgent"
	case "high":
		return "dot-high"
	case "medium":
		return "dot-medium"
	case "low":
		return "dot-low"
	}
	return ""
}

type Comment struct {
	ID        int64
	FeatureID int64
	UserID    int64
	Content   string
	CreatedAt time.Time
	Username  string
	UserRole  string
}

func (c Comment) UserRoleLabel() string {
	switch c.UserRole {
	case "pm":
		return "产品经理"
	case "dev":
		return "开发工程师"
	case "admin":
		return "管理员"
	}
	return c.UserRole
}

type Stats struct {
	Total      int
	Pending    int
	InProgress int
	Done       int
}

type Flash struct {
	Type    string // success | error | info
	Message string
}

// PageData is the top-level template context.
type PageData struct {
	CurrentUser   *User
	ActiveNav     string
	BannerMessage string
	Flash         *Flash
	Data          any
}
