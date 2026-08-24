package skopeo

import (
	"strings"
	"testing"
)

func TestFriendlyCopyError(t *testing.T) {
	cases := []struct {
		name, in string
		want     string
	}{
		{
			name: "架构不匹配",
			in:   `time="..." level=fatal msg="choosing an image from manifest list docker://x: no image found in manifest list for architecture \"amd64\", variant \"\", OS \"linux\""`,
			want: "源镜像不包含所选架构",
		},
		{
			name: "tag 不存在",
			in:   `time="..." level=fatal msg="manifest unknown"`,
			want: "源镜像或 tag 不存在",
		},
		{
			name: "认证失败",
			in:   `unauthorized: authentication required`,
			want: "认证失败",
		},
		{
			name: "未知错误原样返回",
			in:   `some other error`,
			want: "some other error",
		},
	}
	for _, c := range cases {
		got := FriendlyCopyError(c.in)
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: 期望包含 %q,实际: %q", c.name, c.want, got)
		}
	}
}
