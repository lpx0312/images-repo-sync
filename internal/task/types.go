package task

import (
	"errors"

	"gorm.io/gorm"

	"images-repo-sync/internal/crypto"
)

// errAlreadyCanceled 表示任务在 worker 取到前已被用户取消,worker 应跳过不执行。
var errAlreadyCanceled = errors.New("任务已取消")

// syncTaskItemWithRefs 是 worker 执行时需要的 item 最小字段。
// SourceRegistryID/TargetRegistryID 为 0 时沿用任务级字段(旧数据兼容);
// SourceHost 非空表示该条源为匿名仓库(如 docker.io 公开镜像),无需凭证。
type syncTaskItemWithRefs struct {
	ID               uint   `gorm:"column:id"`
	SourceRegistryID uint   `gorm:"column:source_registry_id"`
	TargetRegistryID uint   `gorm:"column:target_registry_id"`
	SourceHost       string `gorm:"column:source_host"`
	SourceRef        string `gorm:"column:source_ref"`
	TargetRef        string `gorm:"column:target_ref"`
}

// taskLoader 用于加载任务执行所需字段。
type taskLoader struct {
	ID               uint   `gorm:"column:id"`
	Arch             string `gorm:"column:arch"`
	Status           string `gorm:"column:status"`
	SourceRegistryID uint   `gorm:"column:source_registry_id"`
	TargetRegistryID uint   `gorm:"column:target_registry_id"`
}

// registryCreds 是任务执行时需要的仓库凭证(已解密)。
type registryCreds struct {
	Host     string
	User     string
	Pass     string
	Insecure bool
	Type     string
	Name     string
}

// loadRegistryCredsByID 按 registry 主键加载凭证并解密密码。
func loadRegistryCredsByID(db *gorm.DB, id uint) (registryCreds, error) {
	var c registryCreds
	var enc string
	err := db.Table("registries").
		Where("id = ?", id).
		Select("host, username, password_enc, insecure, type, name").
		Row().
		Scan(&c.Host, &c.User, &enc, &c.Insecure, &c.Type, &c.Name)
	if err != nil {
		return c, err
	}
	if enc != "" {
		pass, err := crypto.Decrypt(enc)
		if err != nil {
			return c, err
		}
		c.Pass = pass
	}
	return c, nil
}
