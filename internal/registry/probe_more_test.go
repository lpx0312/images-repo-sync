package registry

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------- A 类回归(缺陷已修复,应保持绿色) ----------

// 回归锁定(已修复): A4 部分 registry(如 GitLab 等)的 token 端点仅返回
// {"access_token":"..."} 而无 token 字段,Probe 应同样接受并探测成功。
func TestProbeBearerAccessTokenFieldOnly(t *testing.T) {
	ts := newBearerMock(t,
		func(w http.ResponseWriter, r *http.Request) {
			// token 端点只返回 access_token 字段。
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"access_token":"mock-access-token"}`)
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") == "Bearer mock-access-token" {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
		})
	res := Probe(context.Background(), ts.URL, "alice", "pw", false)
	if !res.OK {
		t.Fatalf("A4: 仅 access_token 字段的 token 响应应被接受: %+v", res)
	}
}

// ---------- B 类边界(GREEN) ----------

// TestProbeRedirectFollowed /v2/ 返回 302 时按最终(跟随重定向后)响应判定。
func TestProbeRedirectFollowed(t *testing.T) {
	// 302 → 200:跟随重定向后成功。
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/real-v2/")
		w.WriteHeader(http.StatusFound) // 302
	})
	mux.HandleFunc("/real-v2/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	res := Probe(context.Background(), ts.URL, "", "", false)
	if !res.OK || !strings.Contains(res.Message, "连接成功") {
		t.Fatalf("302 跟随后 200 应判定成功: %+v", res)
	}

	// 302 → 404:按最终状态码判定为地址错误。
	mux2 := http.NewServeMux()
	mux2.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/gone")
		w.WriteHeader(http.StatusFound)
	})
	mux2.HandleFunc("/gone", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	ts2 := httptest.NewServer(mux2)
	defer ts2.Close()
	res = Probe(context.Background(), ts2.URL, "", "", false)
	if res.OK || !strings.Contains(res.Message, "地址错误") {
		t.Fatalf("302 → 404 应按最终状态报地址错误: %+v", res)
	}
}

// TestProbeConnectionRefusedMessage 连接拒绝(端口未监听)应归类为中文提示。
func TestProbeConnectionRefusedMessage(t *testing.T) {
	res := Probe(context.Background(), "http://127.0.0.1:1", "", "", false)
	if res.OK {
		t.Fatal("未监听端口不应成功")
	}
	if !strings.Contains(res.Message, "连接被拒绝") {
		t.Fatalf("应归类为连接被拒绝的中文提示: %+v", res)
	}
}
