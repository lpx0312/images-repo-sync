package registry

import "testing"

func TestParseChallenge(t *testing.T) {
	cases := []struct {
		name        string
		header      string
		wantRealm   string
		wantService string
		wantOK      bool
	}{
		{
			name:        "标准 Bearer challenge",
			header:      `Bearer realm="https://auth.docker.io/token",service="registry.docker.io"`,
			wantRealm:   "https://auth.docker.io/token",
			wantService: "registry.docker.io",
			wantOK:      true,
		},
		{
			name:        "小写 bearer 也接受",
			header:      `bearer realm="https://harbor.corp.net/service/token",service="harbor-registry"`,
			wantRealm:   "https://harbor.corp.net/service/token",
			wantService: "harbor-registry",
			wantOK:      true,
		},
		{
			name:        "无引号取值",
			header:      `Bearer realm=https://acrs.example.com/oauth2/token,service=acr`,
			wantRealm:   "https://acrs.example.com/oauth2/token",
			wantService: "acr",
			wantOK:      true,
		},
		{
			name:   "缺 realm 不通过",
			header: `Bearer service="only-service"`,
			wantOK: false,
		},
		{
			name:   "Basic challenge 不通过",
			header: `Basic realm="restricted"`,
			wantOK: false,
		},
		{
			name:   "空头不通过",
			header: ``,
			wantOK: false,
		},
	}
	for _, c := range cases {
		realm, service, ok := parseChallenge(c.header)
		if ok != c.wantOK {
			t.Errorf("%s: ok = %v, want %v", c.name, ok, c.wantOK)
			continue
		}
		if ok && (realm != c.wantRealm || service != c.wantService) {
			t.Errorf("%s: got (realm=%q, service=%q), want (%q, %q)",
				c.name, realm, service, c.wantRealm, c.wantService)
		}
	}
}
