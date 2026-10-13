package service

import (
	"testing"
)

func TestResolveT3PairingTarget_DirectLinks(t *testing.T) {
	cases := []struct {
		name           string
		input          string
		wantHTTPBase   string
		wantWSBase     string
		wantCredential string
	}{
		{
			name:           "hash token https",
			input:          "https://t3.example.com/#token=abc123",
			wantHTTPBase:   "https://t3.example.com/",
			wantWSBase:     "wss://t3.example.com/",
			wantCredential: "abc123",
		},
		{
			name:           "query token",
			input:          "https://t3.example.com?token=q-token",
			wantHTTPBase:   "https://t3.example.com/",
			wantWSBase:     "wss://t3.example.com/",
			wantCredential: "q-token",
		},
		{
			name:           "ip literal defaults to http",
			input:          "192.168.1.10:7474/#token=lan-token",
			wantHTTPBase:   "http://192.168.1.10:7474/",
			wantWSBase:     "ws://192.168.1.10:7474/",
			wantCredential: "lan-token",
		},
		{
			name:           "bare host defaults to https",
			input:          "myhost.tailnet.ts.net#token=ts-token",
			wantHTTPBase:   "https://myhost.tailnet.ts.net/",
			wantWSBase:     "wss://myhost.tailnet.ts.net/",
			wantCredential: "ts-token",
		},
		{
			name:           "ws scheme converts to ws/http pair",
			input:          "ws://127.0.0.1:7474/#token=ws-token",
			wantHTTPBase:   "http://127.0.0.1:7474/",
			wantWSBase:     "ws://127.0.0.1:7474/",
			wantCredential: "ws-token",
		},
		{
			name:           "path and search are dropped",
			input:          "https://t3.example.com/some/path?keep=1#token=deep",
			wantHTTPBase:   "https://t3.example.com/",
			wantWSBase:     "wss://t3.example.com/",
			wantCredential: "deep",
		},
		{
			name:           "hash token wins over query token",
			input:          "https://t3.example.com/?token=secondary#token=primary",
			wantHTTPBase:   "https://t3.example.com/",
			wantWSBase:     "wss://t3.example.com/",
			wantCredential: "primary",
		},
		{
			// 官方 `t3 pair` 的实际输出形态：/pair 页面路径 + hash token
			// （panel r1 Grok1 dismissed 判定：清 /pair 路径、不改 origin）。
			name:           "official t3 pair /pair#token form",
			input:          "https://t3.example.com/pair#token=pair-code-1",
			wantHTTPBase:   "https://t3.example.com/",
			wantWSBase:     "wss://t3.example.com/",
			wantCredential: "pair-code-1",
		},
		{
			name:           "official form with port",
			input:          "http://127.0.0.1:7474/pair#token=local-code",
			wantHTTPBase:   "http://127.0.0.1:7474/",
			wantWSBase:     "ws://127.0.0.1:7474/",
			wantCredential: "local-code",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target, err := resolveT3PairingTarget(tc.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if target.HTTPBaseURL != tc.wantHTTPBase {
				t.Fatalf("http base = %q, want %q", target.HTTPBaseURL, tc.wantHTTPBase)
			}
			if target.WSBaseURL != tc.wantWSBase {
				t.Fatalf("ws base = %q, want %q", target.WSBaseURL, tc.wantWSBase)
			}
			if target.Credential != tc.wantCredential {
				t.Fatalf("credential = %q, want %q", target.Credential, tc.wantCredential)
			}
		})
	}
}

func TestResolveT3PairingTarget_HostedWrapper(t *testing.T) {
	// hosted 包装链接：host 参数指向真正后端，token 仍在 hash/query。
	target, err := resolveT3PairingTarget("https://relay.example.com/pair?host=t3host.example.com&label=home&token=wrapped-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.HTTPBaseURL != "https://t3host.example.com/" {
		t.Fatalf("http base = %q, want https://t3host.example.com/", target.HTTPBaseURL)
	}
	if target.WSBaseURL != "wss://t3host.example.com/" {
		t.Fatalf("ws base = %q", target.WSBaseURL)
	}
	if target.Credential != "wrapped-token" {
		t.Fatalf("credential = %q", target.Credential)
	}

	// hosted host 缺协议时按 https 补齐；IP 字面量按 http。
	target, err = resolveT3PairingTarget("https://relay.example.com/?host=10.0.0.2:7474&token=t2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.HTTPBaseURL != "http://10.0.0.2:7474/" {
		t.Fatalf("ip hosted http base = %q", target.HTTPBaseURL)
	}

	// hosted host 也可以是完整 URL。
	target, err = resolveT3PairingTarget("https://relay.example.com/?host=https%3A%2F%2Fenv.example.com%2F&token=t3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.HTTPBaseURL != "https://env.example.com/" {
		t.Fatalf("url hosted http base = %q", target.HTTPBaseURL)
	}
}

func TestResolveT3PairingTarget_Invalid(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"empty", "   "},
		{"missing token", "https://t3.example.com/"},
		{"missing token hosted", "https://relay.example.com/?host=t3.example.com"},
		{"unsupported scheme", "ftp://t3.example.com/#token=x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := resolveT3PairingTarget(tc.input); err == nil {
				t.Fatalf("expected error for %q", tc.input)
			}
		})
	}
}

func TestIsIPLiteral(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"192.168.1.1", true},
		{"127.0.0.1", true},
		{"::1", true},
		{"[::1]", true},
		{"256.1.1.1", false},
		{"example.com", false},
		{"t3.example.com:7474", false},
		{"", false},
		{"1.2.3", false},
	}
	for _, tc := range cases {
		if got := isIPLiteral(tc.host); got != tc.want {
			t.Fatalf("isIPLiteral(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}
