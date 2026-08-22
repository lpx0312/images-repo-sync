package api

import "testing"

// TestHarborRepoName Harbor 返回的 repo 名已含项目前缀,不应重复拼接。
func TestHarborRepoName(t *testing.T) {
	cases := []struct{ project, name, want string }{
		// 新版 Harbor:name 已是 project/repo。
		{"datacenter-test-chart", "datacenter-test-chart/nginx-test", "datacenter-test-chart/nginx-test"},
		// 旧版/代理:name 只有 repo 名。
		{"library", "nginx", "library/nginx"},
		// project 名恰好是 repo 前缀子串也不能误判。
		{"test", "test/nginx", "test/nginx"},
		{"test", "other/nginx", "test/other/nginx"},
	}
	for _, c := range cases {
		if got := harborRepoName(c.project, c.name); got != c.want {
			t.Errorf("harborRepoName(%q,%q) = %q, want %q", c.project, c.name, got, c.want)
		}
	}
}

// TestBuildListRef 浏览目录传入的 repo 名应被锚定到配置仓库的 host。
func TestBuildListRef(t *testing.T) {
	host := "harbor.example.com:7000"
	cases := []struct{ repo, want string }{
		// 浏览目录场景:只给仓库内路径(不含 host),曾会被误判为 docker.io。
		{"datacenter-test-chart/nginx-test", "harbor.example.com:7000/datacenter-test-chart/nginx-test"},
		// 带误配 host 的完整引用:去掉后统一锚定。
		{"harbor.example.com:7000/datacenter-test-chart/nginx-test", "harbor.example.com:7000/datacenter-test-chart/nginx-test"},
		{"docker.io/library/nginx", "harbor.example.com:7000/library/nginx"},
		// 带 tag 的输入应去掉 tag。
		{"library/nginx:1.25", "harbor.example.com:7000/library/nginx"},
		// docker:// 前缀容忍。
		{"docker://library/nginx", "harbor.example.com:7000/library/nginx"},
		// 单段名(docker.io 官方镜像被补 library)。
		{"nginx", "harbor.example.com:7000/library/nginx"},
	}
	for _, c := range cases {
		got, err := buildListRef(host, c.repo)
		if err != nil {
			t.Errorf("buildListRef(%q) 报错: %v", c.repo, err)
			continue
		}
		if got != c.want {
			t.Errorf("buildListRef(%q) = %q, want %q", c.repo, got, c.want)
		}
	}

	// 非法输入。
	if _, err := buildListRef("harbor.example.com", "  "); err == nil {
		t.Errorf("空白 repo 应报错")
	}
	if _, err := buildListRef("", "nginx"); err == nil {
		t.Errorf("空 host 应报错")
	}
	// 名字只有 host 本身,去掉前缀后为空,应报错。
	if _, err := buildListRef("harbor.example.com", "harbor.example.com/"); err == nil {
		t.Errorf("只剩 host 前缀应报错")
	}
}
