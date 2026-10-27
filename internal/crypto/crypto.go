package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"strings"

	"images-repo-sync/internal/config"
)

// cipherPrefix 是密文的版本前缀,用于区分「密文」与「存量明文」:
// 换 key / 清空 key 时不再静默错乱。
const cipherPrefix = "enc:v1:"

// 派生密钥:把任意长度的 ENCRYPT_KEY 经 SHA-256 得到 32 字节 AES-256 密钥。
func deriveKey() []byte {
	h := sha256.Sum256([]byte(config.AppConfig.EncryptKey))
	return h[:]
}

// Enabled 返回是否启用了加密(ENCRYPT_KEY 非空)。
func Enabled() bool { return config.AppConfig.EncryptKey != "" }

// Encrypt 加密明文,返回带版本前缀的密文:enc:v1:<base64(ciphertext+nonce)>。
// 当 ENCRYPT_KEY 为空时,退化为明文返回(调用方应据此告警)。
func Encrypt(plain string) (string, error) {
	if !Enabled() {
		return plain, nil
	}
	block, err := aes.NewCipher(deriveKey())
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ct := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return cipherPrefix + base64.StdEncoding.EncodeToString(ct), nil
}

// Decrypt 解密 Encrypt 的产物,按前缀区分格式:
//   - 带 enc:v1: 前缀:必须是密文,无 key 或解密失败一律报错(换 key 后旧密文不再被静默透传);
//   - 不带前缀且设了 key:尝试按旧格式(裸 base64 密文)解密,失败报错;
//   - 不带前缀且无 key:视为存量明文,原样返回。
func Decrypt(encoded string) (string, error) {
	if strings.HasPrefix(encoded, cipherPrefix) {
		if !Enabled() {
			return "", errors.New("密文解密失败:未配置 ENCRYPT_KEY")
		}
		return decryptLegacy(strings.TrimPrefix(encoded, cipherPrefix))
	}
	if !Enabled() {
		return encoded, nil
	}
	return decryptLegacy(encoded)
}

// decryptLegacy 解密裸 base64(ciphertext+nonce) 格式(不含版本前缀的历史密文)。
func decryptLegacy(encoded string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(deriveKey())
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("密文过短")
	}
	nonce, ct := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}
