package fleet_test

import (
	"testing"

	"github.com/dantech2000/refresh/internal/fleet"
)

func TestParseVersion(t *testing.T) {
	tests := []struct {
		in      string
		want    fleet.Version
		wantErr bool
	}{
		{in: "1.33", want: fleet.Version{Major: 1, Minor: 33}},
		{in: "v1.33", want: fleet.Version{Major: 1, Minor: 33}},
		{in: " 1.33 ", want: fleet.Version{Major: 1, Minor: 33}},
		{in: "1.33.2", want: fleet.Version{Major: 1, Minor: 33}},
		{in: "1.33-eks-abc", want: fleet.Version{Major: 1, Minor: 33}},
		{in: "1.33+build", want: fleet.Version{Major: 1, Minor: 33}},
		{in: "1.9", want: fleet.Version{Major: 1, Minor: 9}},
		{in: "2.0", want: fleet.Version{Major: 2}},
		{in: "", wantErr: true},
		{in: "1", wantErr: true},
		{in: "1.", wantErr: true},
		{in: ".33", wantErr: true},
		{in: "1.-5", wantErr: true},
		{in: "+1.5", wantErr: true},
		{in: "1.x", wantErr: true},
		{in: "latest", wantErr: true},
		{in: "vv1.33", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := fleet.ParseVersion(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseVersion(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("ParseVersion(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

func TestVersion_StringAndCompare(t *testing.T) {
	v := func(major, minor int) fleet.Version { return fleet.Version{Major: major, Minor: minor} }
	if got := v(1, 33).String(); got != "1.33" {
		t.Errorf("String() = %q, want 1.33", got)
	}
	if got := (fleet.Version{}).String(); got != "" {
		t.Errorf("String() of the zero value = %q, want \"\"", got)
	}
	tests := []struct {
		a, b fleet.Version
		want int
	}{
		{v(1, 33), v(1, 33), 0},
		// As numbers, not as text.
		{v(1, 9), v(1, 10), -1},
		{v(1, 34), v(1, 33), 1},
		{v(2, 0), v(1, 99), 1},
		{fleet.Version{}, v(1, 0), -1},
	}
	for _, tt := range tests {
		if got := tt.a.Compare(tt.b); got != tt.want {
			t.Errorf("%v.Compare(%v) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

// A version that parses prints as itself and parses again to the same
// value, and no input makes the parser panic.
func FuzzParseVersion(f *testing.F) {
	for _, seed := range []string{"1.33", "v1.9", "1.-5", "1.33.2-eks", "", "9999999999999999999.1", "1.+3"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		v, err := fleet.ParseVersion(s)
		if err != nil {
			return
		}
		if v.Major < 0 || v.Minor < 0 {
			t.Fatalf("ParseVersion(%q) = %+v: a negative part", s, v)
		}
		if v.IsZero() {
			return // 0.0 prints as "", which is not a version
		}
		again, err := fleet.ParseVersion(v.String())
		if err != nil || again != v {
			t.Fatalf("ParseVersion(%q) = %+v, but its text %q parses to %+v, %v", s, v, v.String(), again, err)
		}
	})
}
