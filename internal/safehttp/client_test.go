package safehttp

import (
	"context"
	"net"
	"net/url"
	"testing"
)

func TestValidateURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "https", raw: "https://downloads.example.org/game.zip", want: true},
		{name: "http", raw: "http://downloads.example.org/file", want: true},
		{name: "credentials", raw: "https://user:secret@downloads.example.org/file"},
		{name: "scheme", raw: "file:///etc/passwd"},
		{name: "missing host", raw: "https:/file"},
		{name: "invalid port", raw: "https://downloads.example.org:70000/file"},
		{name: "IPv6 zone", raw: "http://[fe80::1%25eth0]/file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, err := url.Parse(tt.raw)
			if err != nil {
				t.Fatal(err)
			}
			err = ValidateURL(u)
			if (err == nil) != tt.want {
				t.Fatalf("ValidateURL(%q) error = %v, want allowed=%v", tt.raw, err, tt.want)
			}
		})
	}
}

func TestIsPublicIP(t *testing.T) {
	tests := []struct {
		ip   string
		want bool
	}{
		{"8.8.8.8", true},
		{"10.0.0.1", false},
		{"172.16.0.1", false},
		{"192.168.1.1", false},
		{"127.0.0.1", false},
		{"169.254.169.254", false},
		{"100.64.0.1", false},
		{"198.18.0.1", false},
		{"192.0.2.5", false},
		{"224.0.0.1", false},
		{"2001:4860:4860::8888", true},
		{"::1", false},
		{"fc00::1", false},
		{"fe80::1", false},
		{"2001:db8::1", false},
		{"::ffff:127.0.0.1", false},
	}
	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			if ip == nil {
				t.Fatalf("invalid test IP %q", tt.ip)
			}
			if got := isPublicIP(ip); got != tt.want {
				t.Fatalf("isPublicIP(%s) = %v, want %v", ip, got, tt.want)
			}
		})
	}
}

func TestSafeDialRejectsPrivateDestinations(t *testing.T) {
	if _, err := safeDialContext(context.Background(), "tcp", "127.0.0.1:80"); err == nil {
		t.Fatal("loopback destination should be rejected")
	}
	if _, err := safeDialContext(context.Background(), "tcp", "169.254.169.254:80"); err == nil {
		t.Fatal("link-local metadata destination should be rejected")
	}
}
