package llm

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestBuildEndpoint(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		want    string
		wantErr string
	}{
		{"openai", "https://api.openai.com/v1", "https://api.openai.com/v1/chat/completions", ""},
		{"deepseek", "https://api.deepseek.com", "https://api.deepseek.com/chat/completions", ""},
		{"deepseek trailing slash", "https://api.deepseek.com/", "https://api.deepseek.com/chat/completions", ""},
		{"local", "http://localhost:8080/v1/", "http://localhost:8080/v1/chat/completions", ""},
		{"empty", "", "", "base_url"},
		{"bad scheme", "ftp://example.com/v1", "", "http(s)"},
		{"missing host", "https://", "", "host"},
		{"userinfo", "https://user:pass@example.com/v1", "", "userinfo"},
		{"query", "https://example.com/v1?x=1", "", "query"},
		{"fragment", "https://example.com/v1#x", "", "fragment"},
		{"endpoint included", "https://example.com/v1/chat/completions", "", "/chat/completions"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildEndpoint(tt.baseURL)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("buildEndpoint() error = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("buildEndpoint() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("buildEndpoint() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReadCapped(t *testing.T) {
	data, tooLarge, err := readCapped(strings.NewReader("12345678"), 8)
	if err != nil || tooLarge || string(data) != "12345678" {
		t.Fatalf("exact read = (%q, %v, %v)", data, tooLarge, err)
	}

	data, tooLarge, err = readCapped(strings.NewReader("123456789"), 8)
	if err != nil || !tooLarge || string(data) != "12345678" {
		t.Fatalf("over read = (%q, %v, %v)", data, tooLarge, err)
	}

	data, tooLarge, err = readCapped(strings.NewReader(""), 8)
	if err != nil || tooLarge || len(data) != 0 {
		t.Fatalf("empty read = (%q, %v, %v)", data, tooLarge, err)
	}
}

func TestParseHTTPError(t *testing.T) {
	resp := &http.Response{StatusCode: http.StatusUnauthorized, Status: "401 Unauthorized"}
	httpErr := parseHTTPError(resp, []byte(`{"error":{"message":"bad key","type":"invalid_request_error"}}`))
	if httpErr.StatusCode != 401 || httpErr.Message != "bad key" {
		t.Fatalf("HTTPError = %+v", httpErr)
	}

	httpErr = parseHTTPError(resp, []byte("not-json"))
	if httpErr.Message != "" {
		t.Fatalf("Message = %q, want empty", httpErr.Message)
	}

	long := strings.Repeat("x", maxErrorMessageRunes+10)
	httpErr = parseHTTPError(resp, []byte(`{"error":{"message":"`+long+`"}}`))
	if len([]rune(httpErr.Message)) != maxErrorMessageRunes {
		t.Fatalf("message runes = %d, want %d", len([]rune(httpErr.Message)), maxErrorMessageRunes)
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func TestReadCappedError(t *testing.T) {
	if _, _, err := readCapped(errorReader{}, 8); err == nil {
		t.Fatal("readCapped() error = nil, want error")
	}
}
