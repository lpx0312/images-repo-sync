package skopeo

import (
	"reflect"
	"strings"
	"testing"
)

func TestBuildCopyArgs(t *testing.T) {
	base := CopyOptions{
		SrcRef: "docker.io/library/nginx:1.25",
		DstRef: "harbor.example.com/mirror/nginx:1.25",
	}

	cases := []struct {
		name string
		opt  CopyOptions
		want []string
	}{
		{
			name: "amd64 默认架构",
			opt:  base,
			want: []string{
				"--override-arch=amd64", "--override-os=linux", "copy",
				"docker://docker.io/library/nginx:1.25",
				"docker://harbor.example.com/mirror/nginx:1.25",
			},
		},
		{
			name: "空架构按 amd64",
			opt:  CopyOptions{Arch: "", SrcRef: base.SrcRef, DstRef: base.DstRef},
			want: []string{
				"--override-arch=amd64", "--override-os=linux", "copy",
				"docker://docker.io/library/nginx:1.25",
				"docker://harbor.example.com/mirror/nginx:1.25",
			},
		},
		{
			name: "arm64 单架构",
			opt:  CopyOptions{Arch: ArchARM64, SrcRef: base.SrcRef, DstRef: base.DstRef},
			want: []string{
				"--override-arch=arm64", "--override-os=linux", "copy",
				"docker://docker.io/library/nginx:1.25",
				"docker://harbor.example.com/mirror/nginx:1.25",
			},
		},
		{
			name: "非法架构兜底 amd64",
			opt:  CopyOptions{Arch: "i386", SrcRef: base.SrcRef, DstRef: base.DstRef},
			want: []string{
				"--override-arch=amd64", "--override-os=linux", "copy",
				"docker://docker.io/library/nginx:1.25",
				"docker://harbor.example.com/mirror/nginx:1.25",
			},
		},
		{
			name: "all 全架构:--all 在 copy 之后且无 override",
			opt:  CopyOptions{Arch: ArchAll, SrcRef: base.SrcRef, DstRef: base.DstRef},
			want: []string{
				"copy", "--all",
				"docker://docker.io/library/nginx:1.25",
				"docker://harbor.example.com/mirror/nginx:1.25",
			},
		},
		{
			name: "完整参数:digest 保留 + 双端凭证 + 双端 insecure",
			opt: CopyOptions{
				SrcRef: "gcr.io/foo/bar:v1", SrcAuthFile: "/tmp/src.json", SrcInsecure: true,
				DstRef: "swr.cn-north-4.myhuaweicloud.com/ns/bar:v1", DstAuthFile: "/tmp/dst.json", DstInsecure: true,
				PreserveDigests: true, Arch: ArchAll,
			},
			want: []string{
				"copy", "--all", "--preserve-digests",
				"--src-authfile=/tmp/src.json", "--dest-authfile=/tmp/dst.json",
				"--src-tls-verify=false", "--dest-tls-verify=false",
				"docker://gcr.io/foo/bar:v1",
				"docker://swr.cn-north-4.myhuaweicloud.com/ns/bar:v1",
			},
		},
	}

	for _, c := range cases {
		got := buildCopyArgs(c.opt)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got  %v\n want %v", c.name, got, c.want)
		}
	}

	// 全局 flag 必须在 copy 子命令之前(buildx/skopeo 的硬性要求)。
	for _, arch := range []string{ArchAMD64, ArchARM64} {
		args := buildCopyArgs(CopyOptions{Arch: arch, SrcRef: base.SrcRef, DstRef: base.DstRef})
		if i := strings.Index(args[0], "--override-arch="); i != 0 {
			t.Errorf("arch=%s: 全局 flag 未在最前: %v", arch, args)
		}
		if args[2] != "copy" {
			t.Errorf("arch=%s: copy 子命令位置错误: %v", arch, args)
		}
	}
}

func TestRingBuffer(t *testing.T) {
	// 未填满时按写入顺序拼接。
	r := newRingBuffer(3)
	for _, s := range []string{"a", "b"} {
		r.push(s)
	}
	if got := r.join(","); got != "a,b" {
		t.Errorf("未填满 join = %q, want %q", got, "a,b")
	}

	// 填满后环形覆盖,保留最近 N 行且顺序为最早 -> 最新。
	r = newRingBuffer(3)
	for _, s := range []string{"a", "b", "c", "d", "e"} {
		r.push(s)
	}
	if got := r.join(","); got != "c,d,e" {
		t.Errorf("填满 join = %q, want %q", got, "c,d,e")
	}
}

func TestIsPreserveDigestsConflict(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"time=\"...\" level=fatal msg=\"Instructed to preserve digests, but timestamp validation requires changing them\"", true},
		{"some other error", false},
		{"", false},
	}
	for _, c := range cases {
		if got := IsPreserveDigestsConflict(c.in); got != c.want {
			t.Errorf("IsPreserveDigestsConflict(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
