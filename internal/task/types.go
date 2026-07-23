package task

import (
	"gorm.io/gorm"

	"images-repo-sync/internal/crypto"
)

// syncTaskItemWithRefs 是 worker 执行时需要的 item 最小字段。
type syncTaskItemWithRefs struct {
	ID        uint   `gorm:"column:id"`
	SourceRef string `gorm:"column:source_ref"`
	TargetRef string `gorm:"column:target_ref"`
}

// taskLoader 用于加载任务执行所需字段。
type taskLoader struct {
	ID   uint   `gorm:"column:id"`
	Arch string `gorm:"column:arch"`
}

// registryCreds 是任务执行时需要的仓库凭证(已解密)。
type registryCreds struct {
	Host     string
	User     string
	Pass     string
	Insecure bool
}

// loadRegistryCreds 通过 sync_tasks 表的某 registry 外键列,加载对应仓库的凭证并解密密码。
//
// fkColumn 是 "source_registry_id" 或 "target_registry_id"。
func loadRegistryCreds(db *gorm.DB, table, fkColumn string, taskID uint) (registryCreds, error) {
	var c registryCreds
	row := db.Table(table+" AS t").
		Joins("JOIN registries AS r ON r.id = t."+fkColumn).
		Where("t.id = ?", taskID).
		Select("r.host, r.username, r.password_enc, r.insecure").
		Row()

	var enc string
	var insecure bool
	if err := row.Scan(&c.Host, &c.User, &enc, &insecure); err != nil {
		return c, err
	}
	c.Insecure = insecure
	if enc != "" {
		pass, err := crypto.Decrypt(enc)
		if err != nil {
			return c, err
		}
		c.Pass = pass
	}
	return c, nil
}
