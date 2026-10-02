package awsconfig

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/config"
)

// Profile is one profile in the AWS config or credentials file.
type Profile struct {
	Name string
	// SSO is set when the profile signs in with IAM Identity Center: it has
	// an sso_session or an sso_start_url.
	SSO bool
}

// ConfigFile is the AWS config file: AWS_CONFIG_FILE, else ~/.aws/config.
func ConfigFile() string {
	if p := strings.TrimSpace(os.Getenv("AWS_CONFIG_FILE")); p != "" {
		return p
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".aws", "config")
	}
	return ""
}

// CredentialsFile is the AWS credentials file: AWS_SHARED_CREDENTIALS_FILE,
// else ~/.aws/credentials.
func CredentialsFile() string {
	if p := strings.TrimSpace(os.Getenv("AWS_SHARED_CREDENTIALS_FILE")); p != "" {
		return p
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".aws", "credentials")
	}
	return ""
}

// Profiles lists the profiles in the AWS config and credentials files, in
// file order, each once. It reads names and the SSO keys only (never a
// secret) and skips a file it cannot read.
func Profiles() []Profile {
	var out []Profile
	add := func(p Profile) {
		for i := range out {
			if out[i].Name == p.Name {
				out[i].SSO = out[i].SSO || p.SSO
				return
			}
		}
		out = append(out, p)
	}
	for _, f := range []struct {
		path   string
		config bool
	}{{ConfigFile(), true}, {CredentialsFile(), false}} {
		for _, p := range readProfiles(f.path, f.config) {
			add(p)
		}
	}
	return out
}

// SSOLoginProfile is the profile to log in with for profile ("" for the
// default): the profile at the end of its source_profile chain, as the SDK
// resolves it, when that profile signs in with IAM Identity Center (profile
// itself when it has no source_profile). It returns "" when the SDK takes
// the credentials from something else, or the files cannot be read.
func SSOLoginProfile(ctx context.Context, profile string) string {
	if profile == "" {
		profile = "default"
	}
	files := func(o *config.LoadSharedConfigOptions) {
		o.ConfigFiles = []string{ConfigFile()}
		o.CredentialsFiles = []string{CredentialsFile()}
	}
	sc, err := config.LoadSharedConfigProfile(ctx, profile, files)
	if err != nil {
		return ""
	}
	// The SDK resolves the source_profile chain into Source (a repeated
	// profile keeps only its own credentials) and takes credentials from
	// the end of it, in this order: keys, credential_source,
	// credential_process, then SSO.
	c := &sc
	for i := 0; c.Source != nil && i < 64; i++ {
		c = c.Source
	}
	switch {
	case c.Source != nil, c.Credentials.HasKeys(), c.CredentialSource != "", c.CredentialProcess != "":
		return ""
	case c.SSOSessionName != "" || c.SSOStartURL != "":
		return c.Profile
	}
	return ""
}

// SSOProfiles lists the profiles in the AWS config file (AWS_CONFIG_FILE,
// else ~/.aws/config) that sign in with IAM Identity Center: those with an
// sso_session or sso_start_url. It reads names only and returns nil when the
// file cannot be read.
func SSOProfiles() []string {
	var out []string
	for _, p := range readProfiles(ConfigFile(), true) {
		if p.SSO {
			out = append(out, p.Name)
		}
	}
	return out
}

// readProfiles reads one file's profiles. In the config file a profile is
// [default] or [profile NAME]; in the credentials file it is [NAME].
func readProfiles(path string, config bool) []Profile {
	if path == "" {
		return nil
	}
	f, err := os.Open(path) //nolint:gosec // the user's own AWS config file
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	return scanProfiles(bufio.NewScanner(f), config)
}

func scanProfiles(sc *bufio.Scanner, config bool) []Profile {
	var out []Profile
	cur := -1
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' {
			// A section header, maybe with a comment after it.
			cur = -1
			end := strings.IndexByte(line, ']')
			if end < 0 {
				continue
			}
			// As the SDK reads it: the name keeps its inner spaces.
			name := strings.TrimSpace(line[1:end])
			switch {
			case !config && strings.HasPrefix(name, "profile "):
				continue // the SDK reads [profile x] only in the config file
			case !config && name != "":
			case name == "default":
			case strings.HasPrefix(name, "profile") && len(name) > len("profile") && (name[len("profile")] == ' ' || name[len("profile")] == '\t'):
				name = strings.TrimSpace(name[len("profile"):])
			default:
				continue // [sso-session x], [services x], ...
			}
			out = append(out, Profile{Name: name})
			cur = len(out) - 1
			continue
		}
		// key = value, or key: value.
		sep := strings.IndexAny(line, "=:")
		if sep < 0 || cur < 0 {
			continue
		}
		if k := strings.ToLower(strings.TrimSpace(line[:sep])); k == "sso_session" || k == "sso_start_url" {
			out[cur].SSO = true
		}
	}
	return out
}
