package awsconfig

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/config"
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

[profile   prod  admin  ]
sso_session = acme

[profile colon]
sso_start_url: https://acme.awsapps.com/start

[sso-session acme]
sso_start_url = https://acme.awsapps.com/start
`
	var got []string
	for _, p := range scanProfiles(bufio.NewScanner(strings.NewReader(cfg)), true) {
		if p.SSO {
			got = append(got, p.Name)
		}
	}
	if !slices.Equal(got, []string{"prod-admin", "legacy", "commented", "prod  admin", "colon"}) {
		t.Fatalf("got %v", got)
	}
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", path)
	if got := SSOProfiles(); len(got) != 5 {
		t.Fatalf("SSOProfiles = %v", got)
	}
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "missing"))
	if got := SSOProfiles(); got != nil {
		t.Fatalf("missing file: %v", got)
	}
}

// Profiles reads both files: the config file's [profile NAME] and the
// credentials file's [NAME], each name once, never a secret.
func TestProfilesReadsBothFiles(t *testing.T) {
	dir := t.TempDir()
	cfg, creds := filepath.Join(dir, "config"), filepath.Join(dir, "credentials")
	if err := os.WriteFile(cfg, []byte("[default]\nregion = us-east-1\n[profile sso]\nsso_session = x\n[sso-session x]\nsso_start_url = u\n[profile shared]\nregion = eu-west-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(creds, []byte("[shared]\naws_access_key_id = AKIAEXAMPLE\n[keys-only]\naws_access_key_id = AKIAEXAMPLE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", cfg)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", creds)
	var names []string
	for _, p := range Profiles() {
		names = append(names, p.Name)
		if p.Name == "sso" != p.SSO {
			t.Errorf("%s: SSO = %v", p.Name, p.SSO)
		}
	}
	if !slices.Equal(names, []string{"default", "sso", "shared", "keys-only"}) {
		t.Fatalf("profiles = %v", names)
	}
}

// A profile in neither file names where the name came from and lists the
// profiles that are there.
func TestLoadExplainsAMissingProfile(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config")
	if err := os.WriteFile(cfg, []byte("[profile prod-admin]\nsso_session = acme\n[profile dev]\nregion = us-east-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", cfg)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "none"))
	t.Setenv("AWS_PROFILE", "nope")
	t.Setenv("REFRESH_CONFIG_HOME", dir)
	_, err := Load(t.Context(), nil)
	if err == nil {
		t.Fatal("Load found a profile that does not exist")
	}
	msg := err.Error()
	for _, want := range []string{`AWS profile "nope" not found`, "AWS_PROFILE names it", "prod-admin", "SSO", "dev", "aws configure sso --profile nope"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
	var missing config.SharedConfigProfileNotExistError
	if !errors.As(err, &missing) {
		t.Error("the SDK error is not wrapped")
	}
}

// A role profile logs in through its SSO source_profile: the command must
// name that profile, which has the SSO settings.
func TestSSOLoginProfile(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config")
	if err := os.WriteFile(cfg, []byte(`[profile sso-base]
sso_session = acme
sso_account_id = 111122223333
sso_role_name = R
region = us-east-1
[sso-session acme]
sso_start_url = https://example.awsapps.com/start
sso_region = us-east-1
[profile admin]
role_arn = arn:aws:iam::111122223333:role/admin
source_profile = sso-base
[profile chained]
role_arn = arn:aws:iam::111122223333:role/other
source_profile = admin
[profile keys]
region = us-east-1
[profile both]
role_arn = arn:aws:iam::111122223333:role/both
source_profile = sso-base
sso_session = other
[sso-session other]
sso_start_url = https://other.awsapps.com/start
sso_region = us-east-1
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", cfg)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "none"))
	for profile, want := range map[string]string{"sso-base": "sso-base", "admin": "sso-base", "chained": "sso-base", "keys": "", "missing": "", "both": "sso-base"} {
		if got := SSOLoginProfile(t.Context(), profile); got != want {
			t.Errorf("%s: got %q, want %q", profile, got, want)
		}
	}
}

// A source_profile that is not there names the role profile that refers to
// it, not a context; [profile x] in the credentials file is not listed,
// since the SDK does not read it there.
func TestMissingSourceProfileAndCredentialsSections(t *testing.T) {
	dir := t.TempDir()
	cfg, creds := filepath.Join(dir, "config"), filepath.Join(dir, "credentials")
	if err := os.WriteFile(cfg, []byte("[profile target]\nrole_arn = arn:aws:iam::111122223333:role/x\nsource_profile = typo-base\nregion = us-east-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(creds, []byte("[real]\naws_access_key_id = AKIAEXAMPLE\n[profile wrong]\naws_access_key_id = AKIAEXAMPLE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", cfg)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", creds)
	t.Setenv("AWS_PROFILE", "target")
	t.Setenv("REFRESH_CONFIG_HOME", dir)
	_, err := Load(t.Context(), nil)
	if err == nil || !strings.Contains(err.Error(), `the source_profile of profile "target"`) || strings.Contains(err.Error(), "context") {
		t.Fatalf("err = %v", err)
	}
	// With no profile named, the SDK reads [default], and its chain named
	// the missing profile.
	if err := os.WriteFile(cfg, []byte("[default]\nrole_arn = arn:aws:iam::111122223333:role/x\nsource_profile = typo-base\nregion = us-east-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_PROFILE", "")
	if _, err := Load(t.Context(), nil); err == nil || !strings.Contains(err.Error(), `the source_profile of profile "default"`) {
		t.Fatalf("no profile: err = %v", err)
	}
	var names []string
	for _, p := range Profiles() {
		names = append(names, p.Name)
	}
	if slices.Contains(names, "profile wrong") || !slices.Contains(names, "real") {
		t.Fatalf("profiles = %v", names)
	}
}
