package api

import "testing"

func TestNormalizeHost(t *testing.T) {
	cases := []struct{ in, want string }{
		{"harbor.example.com", "harbor.example.com"},
		{"https://harbor.example.com", "harbor.example.com"},
		{"http://harbor.example.com", "harbor.example.com"},
		{"  harbor.example.com  ", "harbor.example.com"},
		{"harbor.example.com/", "harbor.example.com"},
		{"/harbor.example.com/", "harbor.example.com"},
		{"https://harbor.example.com:5000", "harbor.example.com:5000"},
		{"https://", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := normalizeHost(c.in); got != c.want {
			t.Errorf("normalizeHost(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
