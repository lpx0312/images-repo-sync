package crypto

// b_edge_test 是 B 类边界测试:锁定 crypto 当前正确行为,当前必须全部通过(GREEN)。

import (
	"strings"
	"testing"
)

// TestSameKeyRoundTripBoundary 同一 key 下加解密必须往返一致。
func TestSameKeyRoundTripBoundary(t *testing.T) {
	withKey(t, "same-key-for-roundtrip")
	cases := []string{
		"",
		"p@ssw0rd!",
		"中文密码测试!@#",
		strings.Repeat("long-", 500), // 长明文
	}
	for _, plain := range cases {
		enc, err := Encrypt(plain)
		if err != nil {
			t.Fatalf("Encrypt(%d 字节) 失败: %v", len(plain), err)
		}
		if plain != "" && enc == plain {
			t.Fatalf("设 key 后加密结果不应等于明文: %q", plain)
		}
		got, err := Decrypt(enc)
		if err != nil {
			t.Fatalf("Decrypt 失败: %v", err)
		}
		if got != plain {
			t.Fatalf("同 key 往返不一致: got %q, want %q", got, plain)
		}
	}
}

// TestNoKeyPassthroughBoundary 未设 key 时 Encrypt/Decrypt 均为明文透传(现状锁定)。
func TestNoKeyPassthroughBoundary(t *testing.T) {
	withKey(t, "")
	if Enabled() {
		t.Fatal("未设 key 时 Enabled() 应为 false")
	}
	for _, plain := range []string{"plain-password", "registry-token", ""} {
		enc, err := Encrypt(plain)
		if err != nil || enc != plain {
			t.Errorf("未设 key 时 Encrypt(%q) 应原样返回, got %q err=%v", plain, enc, err)
		}
		dec, err := Decrypt(plain)
		if err != nil || dec != plain {
			t.Errorf("未设 key 时 Decrypt(%q) 应原样返回, got %q err=%v", plain, dec, err)
		}
	}
}
