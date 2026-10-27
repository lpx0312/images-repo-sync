package auth

import "testing"

func TestHashAndCheckPassword(t *testing.T) {
	hash, err := HashPassword("Admin@123")
	if err != nil {
		t.Fatalf("HashPassword 失败: %v", err)
	}
	if hash == "Admin@123" || len(hash) < 20 {
		t.Fatalf("哈希结果异常: %q", hash)
	}
	if !CheckPassword(hash, "Admin@123") {
		t.Fatal("正确密码应校验通过")
	}
	if CheckPassword(hash, "wrong") {
		t.Fatal("错误密码应校验失败")
	}
	if CheckPassword("not-a-hash", "any") {
		t.Fatal("非法哈希格式应校验失败")
	}
}

func TestIsStrongPassword(t *testing.T) {
	cases := []struct {
		pw   string
		want bool
		desc string
	}{
		{"Admin@123", true, "满足全部规则"},
		{"Aa1bbbbbbbbbbb@", true, "另一组合"},
		{"admin123", false, "缺大写"},
		{"ADMIN@123", false, "缺小写"},
		{"Adminabcde", false, "缺数字"},
		{"Admin12345", false, "缺特殊字符"},
		{"Aa1@bcd", false, "不足 8 位(7 位)"},
		{"Abcdefg1!", true, "恰好 8 位"},
		{"", false, "空串"},
		{"中文密码@1aA", true, "中文字符按特殊字符计,规则仍满足"},
	}
	for _, c := range cases {
		if got := IsStrongPassword(c.pw); got != c.want {
			t.Errorf("IsStrongPassword(%q) = %v, want %v (%s)", c.pw, got, c.want, c.desc)
		}
	}
}
