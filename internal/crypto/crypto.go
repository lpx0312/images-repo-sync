package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"

	"images-repo-sync/internal/config"
)

// 派生密钥:把任意长度的 ENCRYPT_KEY 经 SHA-256 得到 32 字节 AES-256 密钥。
func deriveKey() []byte {
	h := sha256.Sum256([]byte(config.AppConfig.EncryptKey))
	return h[:]
}

// Enabled 返回是否启用了加密(ENCRYPT_KEY 非空)。
func Enabled() bool { return config.AppConfig.EncryptKey != "" }

// Encrypt 加密明文,返回 base64(ciphertext+nonce)。
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
	return base64.StdEncoding.EncodeToString(ct), nil
}

// Decrypt 解密 Encrypt 的产物。ENCRYPT_KEY 为空时原样返回。
func Decrypt(encoded string) (string, error) {
	if !Enabled() {
		return encoded, nil
	}
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
