package agent

import "testing"

func TestProviderBaseURLPreservesCustomDeepSeekEndpoint(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		expect string
	}{
		{
			name:   "official root",
			raw:    "https://api.deepseek.com",
			expect: "https://api.deepseek.com",
		},
		{
			name:   "anthropic endpoint",
			raw:    "https://api.deepseek.com/anthropic",
			expect: "https://api.deepseek.com/anthropic",
		},
		{
			name:   "already openai compatible",
			raw:    "https://api.deepseek.com/v1",
			expect: "https://api.deepseek.com/v1",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := getProviderBaseURL("deepseek", tc.raw); got != tc.expect {
				t.Fatalf("getProviderBaseURL(deepseek, %q) = %q, want %q", tc.raw, got, tc.expect)
			}
		})
	}
}

func TestDeepSeekDefaultBaseURLUsesOpenAICompatibleV1Endpoint(t *testing.T) {
	const expect = "https://api.deepseek.com/v1"
	if got := getProviderBaseURL("deepseek", ""); got != expect {
		t.Fatalf("getProviderBaseURL(deepseek, empty) = %q, want %q", got, expect)
	}
}
