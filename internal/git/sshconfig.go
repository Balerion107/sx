package git

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// SSHConfigHostname returns the HostName configured for alias in the
// user's OpenSSH client config (~/.ssh/config), if any. Users commonly
// point short aliases at real git hosts (Host workgit / HostName
// github.com), so a remote like git@workgit:acme/x refers to a
// repository on github.com. Scope matching uses this to compare such
// remotes against scopes stored with the real host.
//
// The parser is deliberately minimal: it handles Host blocks with
// exact (wildcard-free) patterns, both "Key value" and "Key=value"
// forms, and the %h token in HostName. Match blocks, Include
// directives, and wildcard patterns are ignored. The config is parsed
// once per process.
func SSHConfigHostname(alias string) (string, bool) {
	sshConfigOnce.Do(func() {
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		data, err := os.ReadFile(filepath.Join(home, ".ssh", "config"))
		if err != nil {
			return
		}
		sshConfigHosts = parseSSHConfigHosts(string(data))
	})
	hostname, ok := sshConfigHosts[strings.ToLower(alias)]
	return hostname, ok
}

var (
	sshConfigOnce  sync.Once
	sshConfigHosts map[string]string
)

// parseSSHConfigHosts extracts alias → HostName mappings from OpenSSH
// client config content. Only exact Host patterns are recorded;
// following ssh_config's first-obtained-value semantics, the first
// HostName seen for an alias wins.
func parseSSHConfigHosts(content string) map[string]string {
	hosts := make(map[string]string)
	var patterns []string
	inSkippedSection := false

	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := splitSSHConfigLine(line)
		if !ok {
			continue
		}
		switch strings.ToLower(key) {
		case "host":
			patterns = nil
			inSkippedSection = false
			for p := range strings.FieldsSeq(value) {
				if strings.ContainsAny(p, "*?!") {
					continue
				}
				patterns = append(patterns, strings.ToLower(p))
			}
		case "match":
			patterns = nil
			inSkippedSection = true
		case "hostname":
			if inSkippedSection {
				continue
			}
			for _, p := range patterns {
				hostname := strings.ReplaceAll(value, "%h", p)
				if strings.Contains(hostname, "%") {
					continue
				}
				if _, taken := hosts[p]; !taken {
					hosts[p] = strings.ToLower(hostname)
				}
			}
		}
	}
	return hosts
}

// splitSSHConfigLine splits an ssh_config line into keyword and
// argument, accepting both "Key value" and "Key=value" forms.
func splitSSHConfigLine(line string) (key, value string, ok bool) {
	if i := strings.IndexAny(line, " \t="); i > 0 {
		return line[:i], strings.Trim(strings.TrimSpace(line[i+1:]), `"`), true
	}
	return "", "", false
}
