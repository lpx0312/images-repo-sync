package crypto

import (
	"strings"
	"testing"

	"images-repo-sync/internal/config"
)

func withKey(t *testing.T, key string) {
	t.Helper()
	saved := config.AppConfig.EncryptKey
	config.AppConfig.EncryptKey = key
	t.Cleanup(func() { config.AppConfig.EncryptKey = saved })
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	withKey(t, "test-encrypt-key")
	cases := []string{
		"",
		"p@ssw0rd!",
		"中文密码测试",
		strings.Repeat("x", 1000), // 长明文(跨 GCM 分块)
	}
	for _, plain := range cases {
		enc, err := Encrypt(plain)
		if err != nil {
			t.Fatalf("Encrypt(%q) 失败: %v", plain, err)
		}
		if enc == plain && plain != "" {
			t.Fatalf("加密结果不应等于明文")
		}
		got, err := Decrypt(enc)
		if err != nil {
			t.Fatalf("Decrypt 失败: %v", err)
		}
		if got != plain {
			t.Fatalf("往返不一致: got %q, want %q", got, plain)
		}
	}
}

func TestEncryptDifferentEachTime(t *testing.T) {
	withKey(t, "k")
	a, _ := Encrypt("same")
	b, _ := Encrypt("same")
	if a == b {
		t.Fatal("GCM 随机 nonce 应使两次密文不同")
	}
}

func TestDisabledPassthrough(t *testing.T) {
	withKey(t, "") // 未启用加密
	enc, err := Encrypt("plain-password")
	if err != nil || enc != "plain-password" {
		t.Fatalf("未启用加密应原样返回, got %q err=%v", enc, err)
	}
	dec, err := Decrypt("plain-password")
	if err != nil || dec != "plain-password" {
		t.Fatalf("未启用解密应原样返回, got %q err=%v", dec, err)
	}
	if Enabled() {
		t.Fatal("空 key 时 Enabled 应为 false")
	}
}

func TestEnabled(t *testing.T) {
	withKey(t, "some-key")
	if !Enabled() {
		t.Fatal("有 key 时 Enabled 应为 true")
	}
}

func TestDecryptWrongKey(t *testing.T) {
	withKey(t, "key-A")
	enc, err := Encrypt("secret")
	if err != nil {
		t.Fatalf("Encrypt 失败: %v", err)
	}
	config.AppConfig.EncryptKey = "key-B"
	if _, err := Decrypt(enc); err == nil {
		t.Fatal("用错误密钥解密应失败(GCM 认证校验)")
	}
}

func TestDecryptCorruptInput(t *testing.T) {
	withKey(t, "k")
	cases := []struct {
		name, in string
	}{
		{"非 base64", "!!!not-base64!!!"},
		{"空密文(短于 nonce)", "AAAA"},
		{"合法 base64 但随机字节", "AAAAAAAAAAAAAAAAAAAAAA=="},
	}
	for _, c := range cases {
		if _, err := Decrypt(c.in); err == nil {
			t.Errorf("%s: 应解密失败,但成功了", c.name)
		}
	}
}

func TestKeyDerivationDeterministic(t *testing.T) {
	// 任意长度 key 经 SHA-256 派生:同 key 两次加密可互相解(隐式验证派生稳定)。
	withKey(t, strings.Repeat("long-key-", 100))
	enc, err := Encrypt("x")
	if err != nil {
		t.Fatalf("Encrypt 失败: %v", err)
	}
	got, err := Decrypt(enc)
	if err != nil || got != "x" {
		t.Fatalf("长 key 派生往返失败: %q %v", got, err)
	}
}
