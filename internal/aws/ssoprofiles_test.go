package aws

import (
	"bufio"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSSOProfiles(t *testing.T) {
	const cfg = `[default]
region = us-east-1

[profile prod-admin]
sso_session = acme
sso_account_id = 111122223333
sso_role_name = Admin

[profile legacy]
sso_start_url = https://acme.awsapps.com/start
sso_region = us-east-1

[profile keys]
aws_access_key_id = x

[profile commented] # admin access
; a comment
SSO_START_URL = https://acme.awsapps.com/start

[profile   spaced  ]
sso_session = acme

[sso-session acme]
sso_start_url = https://acme.awsapps.com/start
`
	got := ssoProfiles(bufio.NewScanner(strings.NewReader(cfg)))
	if !slices.Equal(got, []string{"prod-admin", "legacy", "commented", "spaced"}) {
		t.Fatalf("got %v", got)
	}
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", path)
	if got := SSOProfiles(); len(got) != 4 {
		t.Fatalf("SSOProfiles = %v", got)
	}
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "missing"))
	if got := SSOProfiles(); got != nil {
		t.Fatalf("missing file: %v", got)
	}
}
