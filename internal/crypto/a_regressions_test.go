package crypto

// a_regressions_test 是代码审查缺陷 A2 的回归测试:
// ENCRYPT_KEY 切换(换 key / 清空 key)时密文无任何格式标记,导致静默错乱。
// 断言「修复后」的行为,对应缺陷已修复,应保持 GREEN。

import (
	"strings"
	"testing"

	"images-repo-sync/internal/config"
)

// TestEncryptOutputHasVersionPrefix 密文应带版本前缀标记。
// 回归锁定(已修复): A2 —— Encrypt 输出没有 `enc:v1:` 前缀,存量数据无法区分
// 「密文」与「明文」,换 key / 清空 key 时只能静默错乱;输出应以 enc:v1: 开头。
func TestEncryptOutputHasVersionPrefix(t *testing.T) {
	withKey(t, "key-A")
	enc, err := Encrypt("secret-password")
	if err != nil {
		t.Fatalf("Encrypt 失败: %v", err)
	}
	if !strings.HasPrefix(enc, "enc:v1:") {
		t.Errorf("回归锁定(已修复) A2: Encrypt 输出应以 enc:v1: 前缀开头, got %q", enc)
	}
}

// TestDecryptAfterKeyRemovalFails 密钥被清空后解密旧密文应报错,而非把密文原文透传当明文。
// 回归锁定(已修复): A2 —— ENCRYPT_KEY 从有切换到无时,Decrypt 走明文透传分支,
// 把 base64 密文原样返回(调用方会拿到一堆乱码当密码用);应返回 error。
func TestDecryptAfterKeyRemovalFails(t *testing.T) {
	withKey(t, "key-A") // t.Cleanup 会恢复原值,这里中途清空是安全的
	enc, err := Encrypt("secret-password")
	if err != nil {
		t.Fatalf("Encrypt 失败: %v", err)
	}

	config.AppConfig.EncryptKey = "" // 模拟运维清空 ENCRYPT_KEY 后重启
	got, err := Decrypt(enc)
	if err == nil {
		t.Errorf("回归锁定(已修复) A2: 清空 key 后 Decrypt(旧密文) 应报错而非透传, got %q err=%v", got, err)
	}
}

// TestDecryptLegacyPlaintextPassthroughNoKey 兼容性:存量无前缀明文在未设 key 时仍可透传。
// (GREEN:现状与修复后都应保持 —— 这也是 enc:v1: 方案不破坏存量明文数据的前提。)
func TestDecryptLegacyPlaintextPassthroughNoKey(t *testing.T) {
	withKey(t, "")
	for _, legacy := range []string{"plain-password", "docker-hub-token", ""} {
		got, err := Decrypt(legacy)
		if err != nil || got != legacy {
			t.Errorf("未设 key 时存量明文 %q 应原样透传, got %q err=%v", legacy, got, err)
		}
	}
}
