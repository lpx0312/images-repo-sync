package skopeo

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteAuthFileWithCreds(t *testing.T) {
	path, err := WriteAuthFile("harbor.example.com", "alice", "s3cret")
	if err != nil {
		t.Fatalf("WriteAuthFile 失败: %v", err)
	}
	defer CleanupAuthFiles(path)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 authfile 失败: %v", err)
	}
	var af struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(data, &af); err != nil {
		t.Fatalf("authfile 不是合法 JSON: %v\n%s", err, data)
	}
	entry, ok := af.Auths["harbor.example.com"]
	if !ok {
		t.Fatalf("应包含 host 条目: %s", data)
	}
	want := base64.StdEncoding.EncodeToString([]byte("alice:s3cret"))
	if entry.Auth != want {
		t.Errorf("auth = %q, want %q", entry.Auth, want)
	}
	// 权限 0600(Windows 上无权限位,跳过检查)。
	if fi, err := os.Stat(path); err == nil && fi.Mode().Perm() != 0o600 {
		// POSIX 上校验;Windows 的 chmod 语义不同,仅在不符且非 Windows 报错。
		if filepath.Separator == '/' {
			t.Errorf("权限 = %v, want 0600", fi.Mode().Perm())
		}
	}
}

func TestWriteAuthFileAnonymous(t *testing.T) {
	// 匿名:user/pass 均空,条目存在但无 auth 字段。
	path, err := WriteAuthFile("public.example.com", "", "")
	if err != nil {
		t.Fatalf("WriteAuthFile 失败: %v", err)
	}
	defer CleanupAuthFiles(path)
	data, _ := os.ReadFile(path)
	var af struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(data, &af); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if e, ok := af.Auths["public.example.com"]; !ok || e.Auth != "" {
		t.Errorf("匿名条目应无 auth: %s", data)
	}
}

func TestCleanupAuthFiles(t *testing.T) {
	p1, _ := WriteAuthFile("h1", "u", "p")
	p2, _ := WriteAuthFile("h2", "u", "p")
	CleanupAuthFiles(p1, p2, "", "not-exist-path")
	for _, p := range []string{p1, p2} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s 应被删除", p)
		}
	}
	// 空串与不存在路径不应 panic。
	CleanupAuthFiles()
}
