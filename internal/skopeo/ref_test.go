package skopeo

import (
	"testing"

	"images-repo-sync/internal/model"
)

func TestParseSource(t *testing.T) {
	cases := []struct {
		in       string
		wantHost string
		wantPath string
	}{
		{"nginx:1.25", "docker.io", "library/nginx:1.25"},
		{"nginx", "docker.io", "library/nginx:latest"},
		{"library/nginx", "docker.io", "library/nginx:latest"},
		{"bitnami/redis:7.0", "docker.io", "bitnami/redis:7.0"},
		{"docker.io/library/nginx:1.25", "docker.io", "library/nginx:1.25"},
		{"gcr.io/k8s-staging/cluster-api/clusterctl:v1", "gcr.io", "k8s-staging/cluster-api/clusterctl:v1"},
		{"harbor.corp.net:5000/ns/img:v1", "harbor.corp.net:5000", "ns/img:v1"},
		{"localhost:5000/myimg:tag", "localhost:5000", "myimg:tag"},
		{"docker://quay.io/jetstack/cert-manager-controller:v1.0", "quay.io", "jetstack/cert-manager-controller:v1.0"},
	}
	for _, c := range cases {
		gotHost, gotPath := ParseSource(c.in)
		if gotHost != c.wantHost || gotPath != c.wantPath {
			t.Errorf("ParseSource(%q) = (%q,%q), want (%q,%q)", c.in, gotHost, gotPath, c.wantHost, c.wantPath)
		}
	}
}

func TestResolveTarget(t *testing.T) {
	const src = "gcr.io/k8s-staging/cluster-api/clusterctl:v1"
	const host = "harbor.example.com"
	const project = "mirror"

	// 用户确认的三个例子:
	// ① flat:           harbor/mirror/clusterctl:v1
	// ② preserve_path:  harbor/mirror/k8s-staging/cluster-api/clusterctl:v1
	// ③ replace_host:   harbor/k8s-staging/cluster-api/clusterctl:v1
	cases := []struct {
		mode    string
		project string
		want    string
	}{
		{model.ModeFlat, project, "harbor.example.com/mirror/clusterctl:v1"},
		{model.ModePreservePath, project, "harbor.example.com/mirror/k8s-staging/cluster-api/clusterctl:v1"},
		{model.ModeReplaceHost, "", "harbor.example.com/k8s-staging/cluster-api/clusterctl:v1"},
		{model.ModeReplaceHost, project, "harbor.example.com/k8s-staging/cluster-api/clusterctl:v1"},
	}
	for _, c := range cases {
		got, err := ResolveTarget(src, host, c.mode, c.project)
		if err != nil {
			t.Fatalf("mode=%s err=%v", c.mode, err)
		}
		if got != c.want {
			t.Errorf("mode=%s got=%q want=%q", c.mode, got, c.want)
		}
	}

	// 隐式 docker.io 边界。
	got, err := ResolveTarget("nginx:1.25", host, model.ModeFlat, project)
	if err != nil || got != "harbor.example.com/mirror/nginx:1.25" {
		t.Errorf("flat on nginx:1.25 got=%q err=%v", got, err)
	}
	got, err = ResolveTarget("bitnami/redis:7.0", host, model.ModeReplaceHost, "")
	if err != nil || got != "harbor.example.com/bitnami/redis:7.0" {
		t.Errorf("replace_host on bitnami/redis got=%q err=%v", got, err)
	}

	// flat / preserve_path 缺 project 应报错。
	if _, err := ResolveTarget(src, host, model.ModeFlat, ""); err == nil {
		t.Error("flat without project should error")
	}
	if _, err := ResolveTarget(src, host, model.ModePreservePath, ""); err == nil {
		t.Error("preserve_path without project should error")
	}
	// 未知模式应报错。
	if _, err := ResolveTarget(src, host, "???", project); err == nil {
		t.Error("unknown mode should error")
	}
}
