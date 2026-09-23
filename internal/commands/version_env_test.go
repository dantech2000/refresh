package commands

import "testing"

func TestEnvTruthy(t *testing.T) {
	tests := map[string]bool{
		"":      false,
		"0":     false,
		"false": false,
		"FALSE": false,
		"no":    false,
		"1":     true,
		"yes":   true,
		"true":  true,
		"on":    true,
	}
	for in, want := range tests {
		if got := envTruthy(in); got != want {
			t.Errorf("envTruthy(%q) = %v, want %v", in, got, want)
		}
	}
}

// REFRESH_NO_UPDATE_CHECK=yes used to fail flag parsing (ParseBool env source)
// and break `refresh version`.
func TestVersionCommandAcceptsNonBoolUpdateCheckEnv(t *testing.T) {
	t.Setenv("REFRESH_NO_UPDATE_CHECK", "yes")
	if err := VersionCommand().Run(t.Context(), []string{"version"}); err != nil {
		t.Fatalf("version with REFRESH_NO_UPDATE_CHECK=yes: %v", err)
	}
}
