package git

import "testing"

func TestParseSSHConfigHosts(t *testing.T) {
	config := `
# comment
Host workgit
    HostName github.com
    User git

Host gh-work github.com-work
	HostName github.com

Host *
    ServerAliveInterval 60

Host wild*
    HostName should-be-skipped.example.com

Host eqform
    HostName=eq.example.com

Host token
    HostName %h.internal.corp

Host badtoken
    HostName %j.example.com

Match host something
    HostName match-skipped.example.com

Host dupe
    HostName first.example.com
Host dupe
    HostName second.example.com

Host CaseHost
    HostName MixedCase.Example.Com
`
	hosts := parseSSHConfigHosts(config)

	tests := []struct {
		alias string
		want  string
		found bool
	}{
		{"workgit", "github.com", true},
		{"gh-work", "github.com", true},
		{"github.com-work", "github.com", true},
		{"eqform", "eq.example.com", true},
		{"token", "token.internal.corp", true},
		{"dupe", "first.example.com", true},
		{"casehost", "mixedcase.example.com", true},
		{"badtoken", "", false},
		{"wildcard-only", "", false},
		{"something", "", false},
		{"missing", "", false},
	}
	for _, tt := range tests {
		got, ok := hosts[tt.alias]
		if ok != tt.found || got != tt.want {
			t.Errorf("hosts[%q] = (%q, %v), want (%q, %v)", tt.alias, got, ok, tt.want, tt.found)
		}
	}
}

func TestSplitSSHConfigLine(t *testing.T) {
	tests := []struct {
		in        string
		key, want string
		ok        bool
	}{
		{"HostName github.com", "HostName", "github.com", true},
		{"HostName=github.com", "HostName", "github.com", true},
		{"HostName\tgithub.com", "HostName", "github.com", true},
		{`HostName "github.com"`, "HostName", "github.com", true},
		{"solo", "", "", false},
	}
	for _, tt := range tests {
		key, value, ok := splitSSHConfigLine(tt.in)
		if key != tt.key || value != tt.want || ok != tt.ok {
			t.Errorf("splitSSHConfigLine(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tt.in, key, value, ok, tt.key, tt.want, tt.ok)
		}
	}
}
