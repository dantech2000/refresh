package aws

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// SSOProfiles lists the profiles in the AWS config file (AWS_CONFIG_FILE,
// else ~/.aws/config) that sign in with IAM Identity Center: those with an
// sso_session or sso_start_url. It reads names only and returns nil when the
// file cannot be read.
func SSOProfiles() []string {
	path := strings.TrimSpace(os.Getenv("AWS_CONFIG_FILE"))
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		path = filepath.Join(home, ".aws", "config")
	}
	f, err := os.Open(path) //nolint:gosec // the user's own AWS config file
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	return ssoProfiles(bufio.NewScanner(f))
}

func ssoProfiles(sc *bufio.Scanner) []string {
	var out []string
	profile, added := "", false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' {
			// A section header, maybe with a comment after it.
			profile, added = "", false
			end := strings.IndexByte(line, ']')
			if end < 0 {
				continue
			}
			name := strings.Join(strings.Fields(line[1:end]), " ")
			switch {
			case name == "default":
				profile = name
			case strings.HasPrefix(name, "profile "):
				profile = strings.TrimSpace(strings.TrimPrefix(name, "profile "))
			}
			continue
		}
		key, _, ok := strings.Cut(line, "=")
		if !ok || profile == "" || added {
			continue
		}
		if k := strings.ToLower(strings.TrimSpace(key)); k == "sso_session" || k == "sso_start_url" {
			out = append(out, profile)
			added = true
		}
	}
	return out
}
