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

// Chart 仓库类型常量。
const (
	ChartRepoTypeOCI         = "oci"         // OCI 兼容 registry(Harbor 等),chart 作为 OCI artifact 推送
	ChartRepoTypeChartMuseum = "chartmuseum" // ChartMuseum,走 POST /api/charts
)

// 登录日志状态常量。
const (
	LoginStatusSuccess = "success"
	LoginStatusFailed  = "failed"
)

// User 是登录用户。单 admin 角色,不做多角色 RBAC。
// PwdChangedAt 记录最近一次改密时间:早于该时间签发的 JWT 一律失效(改密即吊销旧 token)。
type User struct {
	ID           uint       `json:"id" gorm:"primaryKey"`
	Username     string     `json:"username" gorm:"type:varchar(50);uniqueIndex;not null"`
	PasswordHash string     `json:"-" gorm:"type:varchar(255);not null"`
	Status       string     `json:"status" gorm:"type:varchar(20);default:'active'"`
	PwdChangedAt time.Time  `json:"-" gorm:"column:pwd_changed_at"`
	LastLoginAt  *time.Time `json:"last_login_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

func (User) TableName() string { return "users" }

// Registry 是源/目标仓库配置。一个 registry 对应一组(host, 用户名, 密码)。
// 仓库本身不分角色(源/目标在每次同步任务时选择),也不绑定 project
// (DefaultProject 仅作为 ACR 等无法列 namespace 仓库的输入兜底默认值)。
// InSourceGroup 标记加入「源镜像组」:粘贴列表创建任务时,按镜像地址的主机名
// 到这组仓库里匹配源凭证(见 task.Create 的自动匹配逻辑)。
type Registry struct {
	ID             uint      `json:"id" gorm:"primaryKey"`
	Name           string    `json:"name" gorm:"type:varchar(100);uniqueIndex;not null"`
	Host           string    `json:"host" gorm:"type:varchar(255);not null"`
	Type           string    `json:"type" gorm:"type:varchar(20);default:'generic'"`
	Username       string    `json:"username" gorm:"type:varchar(255)"`
	PasswordEnc    string    `json:"-" gorm:"column:password_enc;type:varchar(512)"`
	Insecure       bool      `json:"insecure" gorm:"default:false"`
	DefaultProject string    `json:"default_project" gorm:"type:varchar(255)"`
	InSourceGroup  bool      `json:"in_source_group" gorm:"default:false"`
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
	Status           string         `json:"status" gorm:"type:varchar(20);default:'pending';index:idx_sync_tasks_status_created_at,priority:1"`
	Error            string         `json:"error" gorm:"type:text"`
	CreatedBy        uint           `json:"created_by" gorm:"default:0"`
	CreatedAt        time.Time      `json:"created_at" gorm:"index:idx_sync_tasks_status_created_at,priority:2"`
	StartedAt        *time.Time     `json:"started_at"`
	FinishedAt       *time.Time     `json:"finished_at"`
	Items            []SyncTaskItem `json:"items" gorm:"foreignKey:TaskID"`
}

func (SyncTask) TableName() string { return "sync_tasks" }

// SyncTaskItem 是任务中的一条镜像同步明细。
// 多目标/自动匹配源的任务在创建时就把每条明细展开为「镜像 × 目标」并记录各自的
// 源/目标仓库 ID;两列为 0 且 SourceHost 为空时表示沿用任务级字段(旧数据兼容)。
// SourceHost 非空表示该条源为匿名仓库(目前仅 docker.io 公开镜像,无需凭证)。
type SyncTaskItem struct {
	ID               uint       `json:"id" gorm:"primaryKey"`
	TaskID           uint       `json:"task_id" gorm:"not null;index"`
	SourceRegistryID uint       `json:"source_registry_id" gorm:"default:0"`
	TargetRegistryID uint       `json:"target_registry_id" gorm:"default:0"`
	SourceHost       string     `json:"source_host" gorm:"type:varchar(255)"`
	SourceRef        string     `json:"source_ref" gorm:"type:varchar(512);not null"`
	TargetRef        string     `json:"target_ref" gorm:"type:varchar(512);not null"`
	Status           string     `json:"status" gorm:"type:varchar(20);default:'pending'"`
	Error            string     `json:"error" gorm:"type:text"`
	Digest           string     `json:"digest" gorm:"type:varchar(255)"`
	CreatedAt        time.Time  `json:"created_at"`
	StartedAt        *time.Time `json:"started_at"`
	FinishedAt       *time.Time `json:"finished_at"`
}

func (SyncTaskItem) TableName() string { return "sync_task_items" }

// ChartRepo 是 chart 仓库配置(OCI registry 或 ChartMuseum)。
// Host 为仓库地址,可带 http:// 前缀表示走明文 HTTP(默认 https);
// Project 为 chart 存放的项目/命名空间路径,如 "datacenter-test-chart",
// OCI 推送目标为 host/project/<chartName>:<chartVersion>,ChartMuseum 则透传给其 API。
type ChartRepo struct {
	ID          uint      `json:"id" gorm:"primaryKey"`
	Name        string    `json:"name" gorm:"type:varchar(100);uniqueIndex;not null"`
	Type        string    `json:"type" gorm:"type:varchar(20);not null"` // oci / chartmuseum
	Host        string    `json:"host" gorm:"type:varchar(255);not null"`
	Project     string    `json:"project" gorm:"type:varchar(255)"`
	Username    string    `json:"username" gorm:"type:varchar(255)"`
	PasswordEnc string    `json:"-" gorm:"column:password_enc;type:varchar(512)"`
	Insecure    bool      `json:"insecure" gorm:"default:false"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (ChartRepo) TableName() string { return "chart_repos" }

// ChartUpload 是一次 chart 包上传的记录(单文件一条)。
// RepoName/RepoType/TargetRef 在创建时快照,仓库配置后续修改/删除不影响历史记录展示。
type ChartUpload struct {
	ID           uint       `json:"id" gorm:"primaryKey"`
	ChartRepoID  uint       `json:"chart_repo_id" gorm:"not null;index"`
	RepoName     string     `json:"repo_name" gorm:"type:varchar(100)"`
	RepoType     string     `json:"repo_type" gorm:"type:varchar(20)"`
	TargetRef    string     `json:"target_ref" gorm:"type:varchar(512)"` // oci://host/project/name:version 或 host/api/charts
	FileName     string     `json:"file_name" gorm:"type:varchar(255)"`
	FilePath     string     `json:"file_path" gorm:"type:varchar(512)"` // 服务端文件路径(上传临时文件或用户填写的路径)
	IsTemp       bool       `json:"is_temp" gorm:"default:false"`       // 浏览器上传的临时文件:推送成功后删除,失败保留以便重试
	ChartName    string     `json:"chart_name" gorm:"type:varchar(255)"`
	ChartVersion string     `json:"chart_version" gorm:"type:varchar(64)"`
	Size         int64      `json:"size"`
	Status       string     `json:"status" gorm:"type:varchar(20);default:'pending';index"`
	Error        string     `json:"error" gorm:"type:text"`
	Digest       string     `json:"digest" gorm:"type:varchar(255)"` // OCI 推送成功后的 manifest digest
	CreatedBy    uint       `json:"created_by" gorm:"default:0"`
	CreatedAt    time.Time  `json:"created_at"`
	FinishedAt   *time.Time `json:"finished_at"`
}

func (ChartUpload) TableName() string { return "chart_uploads" }

// LoginLog 记录登录审计。CreatedAt 建索引:审计列表按时间倒序分页的高频查询。
type LoginLog struct {
	ID        uint      `json:"id" gorm:"primaryKey"`
	UserID    uint      `json:"user_id" gorm:"index"`
	Username  string    `json:"username" gorm:"type:varchar(50);index"`
	IP        string    `json:"ip" gorm:"type:varchar(64)"`
	UserAgent string    `json:"user_agent" gorm:"type:varchar(255)"`
	Status    string    `json:"status" gorm:"type:varchar(20);index"`
	Message   string    `json:"message" gorm:"type:varchar(255)"`
	CreatedAt time.Time `json:"created_at" gorm:"index"`
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
