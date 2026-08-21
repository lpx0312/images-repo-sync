package model

import "time"

// 用户状态常量。
const (
	UserStatusActive   = "active"
	UserStatusDisabled = "disabled"
)

// 任务状态常量。
const (
	TaskStatusPending  = "pending"
	TaskStatusRunning  = "running"
	TaskStatusSuccess  = "success"
	TaskStatusFailed   = "failed"
	TaskStatusCanceled = "canceled"
)

// 任务明细状态常量。
const (
	ItemStatusPending = "pending"
	ItemStatusRunning = "running"
	ItemStatusSuccess = "success"
	ItemStatusFailed  = "failed"
	ItemStatusSkipped = "skipped"
)

// 同步模式常量。
const (
	ModeFlat         = "flat"          // 进配置 project,只保留镜像名+tag
	ModePreservePath = "preserve_path" // 进配置 project,源 host 后完整路径原样保留
	ModeReplaceHost  = "replace_host"  // 不加 project 前缀,只换 host
)

// 仓库类型常量。
const (
	RegistryTypeGeneric   = "generic"   // 通用 OCI/Docker Registry v2
	RegistryTypeHarbor    = "harbor"    // Harbor(走 v2 API 列 project/repo)
	RegistryTypeDockerHub = "dockerhub" // Docker Hub
	RegistryTypeACR       = "acr"       // 阿里云 ACR
	RegistryTypeSWR       = "swr"       // 华为云 SWR:拒收顶层 OCI image index,作目标时需允许格式转换
)

// 登录日志状态常量。
const (
	LoginStatusSuccess = "success"
	LoginStatusFailed  = "failed"
)

// User 是登录用户。单 admin 角色,不做多角色 RBAC。
type User struct {
	ID           uint       `json:"id" gorm:"primaryKey"`
	Username     string     `json:"username" gorm:"type:varchar(50);uniqueIndex;not null"`
	PasswordHash string     `json:"-" gorm:"type:varchar(255);not null"`
	Status       string     `json:"status" gorm:"type:varchar(20);default:'active'"`
	LastLoginAt  *time.Time `json:"last_login_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

func (User) TableName() string { return "users" }

// Registry 是源/目标仓库配置。一个 registry 对应一组(host, 用户名, 密码)。
// 仓库本身不分角色(源/目标在每次同步任务时选择),也不绑定 project
// (DefaultProject 仅作为 ACR 等无法列 namespace 仓库的输入兜底默认值)。
type Registry struct {
	ID             uint      `json:"id" gorm:"primaryKey"`
	Name           string    `json:"name" gorm:"type:varchar(100);uniqueIndex;not null"`
	Host           string    `json:"host" gorm:"type:varchar(255);not null"`
	Type           string    `json:"type" gorm:"type:varchar(20);default:'generic'"`
	Username       string    `json:"username" gorm:"type:varchar(255)"`
	PasswordEnc    string    `json:"-" gorm:"column:password_enc;type:varchar(512)"`
	Insecure       bool      `json:"insecure" gorm:"default:false"`
	DefaultProject string    `json:"default_project" gorm:"type:varchar(255)"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func (Registry) TableName() string { return "registries" }

// SyncTask 是一次同步任务。
type SyncTask struct {
	ID               uint           `json:"id" gorm:"primaryKey"`
	SourceRegistryID uint           `json:"source_registry_id" gorm:"not null;index"`
	TargetRegistryID uint           `json:"target_registry_id" gorm:"not null;index"`
	Mode             string         `json:"mode" gorm:"type:varchar(20);not null"`
	TargetProject    string         `json:"target_project" gorm:"type:varchar(255)"`
	Arch             string         `json:"arch" gorm:"type:varchar(10);default:'amd64'"` // amd64 / arm64 / all
	Total            int            `json:"total" gorm:"default:0"`
	Succeeded        int            `json:"succeeded" gorm:"default:0"`
	Failed           int            `json:"failed" gorm:"default:0"`
	Skipped          int            `json:"skipped" gorm:"default:0"`
	Status           string         `json:"status" gorm:"type:varchar(20);default:'pending';index"`
	Error            string         `json:"error" gorm:"type:text"`
	CreatedBy        uint           `json:"created_by" gorm:"default:0"`
	CreatedAt        time.Time      `json:"created_at"`
	StartedAt        *time.Time     `json:"started_at"`
	FinishedAt       *time.Time     `json:"finished_at"`
	Items            []SyncTaskItem `json:"items" gorm:"foreignKey:TaskID"`
}

func (SyncTask) TableName() string { return "sync_tasks" }

// SyncTaskItem 是任务中的一条镜像同步明细。
type SyncTaskItem struct {
	ID         uint       `json:"id" gorm:"primaryKey"`
	TaskID     uint       `json:"task_id" gorm:"not null;index"`
	SourceRef  string     `json:"source_ref" gorm:"type:varchar(512);not null"`
	TargetRef  string     `json:"target_ref" gorm:"type:varchar(512);not null"`
	Status     string     `json:"status" gorm:"type:varchar(20);default:'pending'"`
	Error      string     `json:"error" gorm:"type:text"`
	Digest     string     `json:"digest" gorm:"type:varchar(255)"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

func (SyncTaskItem) TableName() string { return "sync_task_items" }

// LoginLog 记录登录审计。
type LoginLog struct {
	ID        uint      `json:"id" gorm:"primaryKey"`
	UserID    uint      `json:"user_id" gorm:"index"`
	Username  string    `json:"username" gorm:"type:varchar(50);index"`
	IP        string    `json:"ip" gorm:"type:varchar(64)"`
	UserAgent string    `json:"user_agent" gorm:"type:varchar(255)"`
	Status    string    `json:"status" gorm:"type:varchar(20);index"`
	Message   string    `json:"message" gorm:"type:varchar(255)"`
	CreatedAt time.Time `json:"created_at"`
}

func (LoginLog) TableName() string { return "login_logs" }

// SystemSetting 是键值对形式的系统配置。
// key 形如 "default_arch";value 为字符串,由调用方解释。
type SystemSetting struct {
	Key   string `json:"key" gorm:"primaryKey;type:varchar(64)"`
	Value string `json:"value" gorm:"type:varchar(255)"`
}

func (SystemSetting) TableName() string { return "system_settings" }

// 已知的系统配置键。
const (
	SettingKeyDefaultArch = "default_arch" // 默认同步架构: amd64 / arm64 / all
)
