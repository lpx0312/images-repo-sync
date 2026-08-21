package registry

import (
	"crypto/tls"
	"encoding/base64"
	"net/http"
	"time"
)

// 共享的 registry HTTP 调用工具:client 与 Basic Auth 头。
// probe 与 catalog(api 包)共用,避免各自维护一份实现。

// 默认与跳过 TLS 校验各一个共享 client(复用连接池)。
// 20s 超时对探测与列表接口够用;跳过校验仅用于自签证书的私有仓库。
var (
	defaultHTTPClient  = &http.Client{Timeout: 20 * time.Second}
	insecureHTTPClient = &http.Client{
		Timeout:   20 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
)

// HTTPClient 返回调用 registry API 用的 client;insecure 为 true 时跳过 TLS 证书校验。
func HTTPClient(insecure bool) *http.Client {
	if insecure {
		return insecureHTTPClient
	}
	return defaultHTTPClient
}

// BasicAuthHeader 返回 "Basic base64(user:pass)";user 与 pass 均为空时返回空串(匿名)。
func BasicAuthHeader(user, pass string) string {
	if user == "" && pass == "" {
		return ""
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}
