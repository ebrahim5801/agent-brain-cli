package redact

import (
	"strings"
	"testing"
)

// mailgunFixture is assembled at run time rather than written out: a literal
// "key-" followed by 32 hex characters trips GitHub's push protection as a real
// Mailgun credential, and this repo is public. The value is fake either way.
var mailgunFixture = "key-" + strings.Repeat("0123456789abcdef", 2)

func TestApply(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"aws key", "the key is AKIAIOSFODNN7EXAMPLE ok", "the key is [redacted:aws-key] ok"},
		{"github token", "use ghp_abcdefghij1234567890KLMNOP here", "use [redacted:github-token] here"},
		{"gitlab token", "glpat-AbCdEf123456789012345 works", "[redacted:gitlab-token] works"},
		{"slack token", "xoxb-123456789012-abcdefghij", "[redacted:slack-token]"},
		{"stripe key", "sk_live_abcdefghijklmnop1234", "[redacted:stripe-key]"},
		{"google key", "AIzaSyA1234567890abcdefghijklmnopqrstuv", "[redacted:google-key]"},
		{"anthropic key", "sk-ant-api03-AbCdEf1234567890_xyz-QQ", "[redacted:anthropic-key]"},
		{"openai key", "sk-proj-AbCdEf1234567890xyzQQ", "[redacted:openai-key]"},
		{"openai legacy key", "sk-AbCdEf1234567890xyzQQrs", "[redacted:openai-key]"},
		{"npm token", "npm_abcdefghij1234567890ABCDEFGHIJ123456", "[redacted:npm-token]"},
		{"huggingface token", "hf_abcdefghij1234567890ABCDEFGHIJ", "[redacted:huggingface-token]"},
		{"sendgrid key", "SG.abcdefghij1234567890.abcdefghij1234567890", "[redacted:sendgrid-key]"},
		{"mailgun key", mailgunFixture, "[redacted:mailgun-key]"},
		{"git sha untouched by vendor rules", "commit 5f2e1a9c3b7d4e6f8a0b2c4d6e8f0a2b4c6d8e0f stands", "commit 5f2e1a9c3b7d4e6f8a0b2c4d6e8f0a2b4c6d8e0f stands"},
		{"prose starting with sk- is not a key", "sk-a is short", "sk-a is short"},
		{
			"private key block",
			"-----BEGIN RSA PRIVATE KEY-----\nMIIEow\n-----END RSA PRIVATE KEY-----",
			"[redacted:private-key]",
		},
		{
			"password assignment",
			`we set password = "hunter2secret" in the env`,
			`we set password = "[redacted:credential]" in the env`,
		},
		{
			"api key assignment",
			"API_KEY: abc123def456ghi789",
			"API_KEY: [redacted:credential]",
		},
		{
			"bearer header",
			"Authorization: Bearer abcdef1234567890abcdef",
			"Authorization: Bearer [redacted:bearer]",
		},
		{
			"url credential",
			"connect to postgres://user:s3cretpw@localhost:5432/db",
			"connect to postgres://user:[redacted:url-credential]@localhost:5432/db",
		},
		{
			"prose untouched",
			"We decided to use pgx directly; retries use exponential backoff.",
			"We decided to use pgx directly; retries use exponential backoff.",
		},
		{
			"git sha untouched",
			"captured at commit 4f2a9c8e1b7d3f6a0c5e8b2d9f1a4c7e0b3d6f9a on main",
			"captured at commit 4f2a9c8e1b7d3f6a0c5e8b2d9f1a4c7e0b3d6f9a on main",
		},
		{
			"jwt",
			"token eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
			"token [redacted:jwt]",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Apply(tc.in)
			if got != tc.want {
				t.Errorf("Apply(%q)\n got: %q\nwant: %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestApplyNeverLeaksOriginal(t *testing.T) {
	secrets := []string{
		"AKIAIOSFODNN7EXAMPLE",
		"ghp_abcdefghij1234567890KLMNOP",
		"sk_live_abcdefghijklmnop1234",
		"sk-ant-api03-AbCdEf1234567890_xyz-QQ",
		"sk-proj-AbCdEf1234567890xyzQQ",
		"npm_abcdefghij1234567890ABCDEFGHIJ123456",
		"hf_abcdefghij1234567890ABCDEFGHIJ",
		"SG.abcdefghij1234567890.abcdefghij1234567890",
		mailgunFixture,
	}
	for _, s := range secrets {
		if got := Apply("value " + s + " end"); strings.Contains(got, s) {
			t.Errorf("secret %q survived redaction: %q", s, got)
		}
	}
}
