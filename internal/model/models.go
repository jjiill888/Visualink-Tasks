package model

import (
	"time"
)

var shanghaiLoc *time.Location

func init() {
	var err error
	shanghaiLoc, err = time.LoadLocation("Asia/Shanghai")
	if err != nil {
		shanghaiLoc = time.UTC
	}
}

type User struct {
	ID          int64
	Username    string // @mention handle, no spaces allowed
	DisplayName string // display name shown in UI, can have spaces
	Email       string
	Password    string
	Role        string // pm | dev | admin
	CreatedAt   time.Time
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
	Status      string // pending | in_progress | done | rejected | archived
	CreatedBy   int64
	AssignedTo  *int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
	// Joined
	CreatorName string
	CreatorRole string
	GroupTitle  string
	// Computed
	IsWatched bool
}

type GroupMember struct {
	UserID      int64
	DisplayName string
	Role        string
	Type        string // member | watch
}

type GroupSubscription struct {
	GroupID    int64
	GroupTitle string
	Type       string // member | watch
}

func (f Feature) CreatedAtLocal() string {
	return f.CreatedAt.In(shanghaiLoc).Format("2006-01-02 15:04")
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
	case "draft":
		return "草稿"
	case "pending":
		return "待处理"
	case "in_progress":
		return "进行中"
	case "done":
		return "已完成"
	case "rejected":
		return "已驳回"
	case "archived":
		return "已归档"
	}
	return f.Status
}

func (f Feature) StatusBadgeClass() string {
	switch f.Status {
	case "draft":
		return "badge-draft"
	case "pending":
		return "badge-pending"
	case "in_progress":
		return "badge-progress"
	case "done":
		return "badge-done"
	case "rejected":
		return "badge-rejected"
	case "archived":
		return "badge-archived"
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

type FeatureEvent struct {
	ID         int64
	FeatureID  int64
	OperatorID int64
	Action     string // created | status_changed
	OldValue   string
	NewValue   string
	CreatedAt  time.Time
	// Joined
	OperatorName string
	OperatorRole string
}

func statusLabel(s string) string {
	switch s {
	case "draft":
		return "草稿"
	case "pending":
		return "待处理"
	case "in_progress":
		return "进行中"
	case "done":
		return "已完成"
	case "rejected":
		return "已驳回"
	case "archived":
		return "已归档"
	}
	return s
}

func (e *FeatureEvent) ActionDesc() string {
	switch e.Action {
	case "created":
		return "提交了此功能"
	case "status_changed":
		return "将状态从「" + statusLabel(e.OldValue) + "」改为「" + statusLabel(e.NewValue) + "」"
	}
	return e.Action
}

func (e *FeatureEvent) CreatedAtLocal() string {
	return e.CreatedAt.In(shanghaiLoc).Format("2006-01-02 15:04")
}

func (e *FeatureEvent) OperatorRoleLabel() string {
	switch e.OperatorRole {
	case "pm":
		return "产品经理"
	case "dev":
		return "开发工程师"
	case "admin":
		return "管理员"
	}
	return e.OperatorRole
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

type Notification struct {
	ID           int64
	UserID       int64
	FeatureID    int64
	CommentID    int64
	FromUser     string
	FeatureTitle string
	Message      string
	IsRead       bool
	CreatedAt    time.Time
}

func (n Notification) CreatedAtLocal() string {
	return n.CreatedAt.In(shanghaiLoc).Format("01-02 15:04")
}

func (n Notification) PreviewText() string {
	if n.Message != "" {
		return n.Message
	}
	if n.FeatureTitle == "" {
		return "你收到了一条系统通知"
	}
	return "在「" + n.FeatureTitle + "」里提到了你"
}

func MentionNotificationText(featureTitle string) string {
	if featureTitle == "" {
		return "提到了你"
	}
	return "在「" + featureTitle + "」里提到了你"
}

func FeatureStatusNotificationText(featureTitle, status string, automatic bool) string {
	target := "你提交的功能"
	if featureTitle != "" {
		target += "「" + featureTitle + "」"
	}
	switch status {
	case "in_progress":
		return "已受理" + target
	case "rejected":
		return "驳回了" + target
	case "done":
		return "已完成" + target
	case "archived":
		if automatic {
			return "已自动归档" + target
		}
		return "已归档" + target
	default:
		return "更新了" + target + "的状态为「" + statusLabel(status) + "」"
	}
}

type DirectMessage struct {
	ID            int64
	SenderID      int64
	RecipientID   int64
	Content       string
	IsRead        bool
	CreatedAt     time.Time
	SenderName    string
	RecipientName string
}

func (m DirectMessage) CreatedAtLocal() string {
	return m.CreatedAt.In(shanghaiLoc).Format("01-02 15:04")
}

type MessageContact struct {
	Kind        string
	UserID      int64
	Title       string
	Secondary   string
	Preview     string
	UnreadCount int
	LastAt      time.Time
	Active      bool
	Empty       bool
}

func (c MessageContact) Key() string {
	if c.Kind == "system" {
		return "system"
	}
	return c.Kind
}

func (c MessageContact) LastAtLabel() string {
	if c.LastAt.IsZero() {
		return ""
	}
	return c.LastAt.In(shanghaiLoc).Format("01-02 15:04")
}

type MessagePreview struct {
	Count int
	Items []*MessageContact
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
