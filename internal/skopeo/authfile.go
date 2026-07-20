package skopeo

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
)

// authFile 对应 containers-auth.json(5) / docker config.json 格式。
type authFile struct {
	Auths map[string]authEntry `json:"auths"`
}

type authEntry struct {
	Auth string `json:"auth,omitempty"`
}

// WriteAuthFile 生成一个临时 auth.json,写入 host -> base64(user:pass)。
// 返回文件路径;调用方应在用完后调用 os.Remove(path)。
// user/pass 为空时,该 host 仍写入但 auth 为空(匿名访问场景)。
func WriteAuthFile(host, user, pass string) (string, error) {
	af := authFile{Auths: map[string]authEntry{}}
	if user != "" || pass != "" {
		af.Auths[host] = authEntry{
			Auth: base64.StdEncoding.EncodeToString([]byte(user + ":" + pass)),
		}
	} else {
		af.Auths[host] = authEntry{}
	}

	f, err := os.CreateTemp("", "skopeo-auth-*.json")
	if err != nil {
		return "", err
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(af); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	// 0600,仅属主可读,避免凭证泄露。
	if err := os.Chmod(f.Name(), 0o600); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// CleanupAuthFiles 删除给定路径列表中存在的文件,忽略错误。
func CleanupAuthFiles(paths ...string) {
	for _, p := range paths {
		if p != "" {
			_ = os.Remove(filepath.Clean(p))
		}
	}
}
