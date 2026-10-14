package awsx

import "testing"

func TestCheckRegion(t *testing.T) {
	valid := []string{
		"us-east-1", "us-gov-west-1", "cn-north-1", "cn-northwest-1", "eu-central-2",
		"ap-southeast-7", "mx-central-1", "il-central-1", "us-iso-east-1", "us-isob-east-1",
		"eu-isoe-west-1", "eusc-de-east-1", "us-east-9",
	}
	for _, name := range valid {
		if err := CheckRegion(name); err != nil {
			t.Errorf("CheckRegion(%q) = %v, want nil", name, err)
		}
	}
	invalid := []string{
		"", "notaregion", "bogus-1", "bogus-region-1", "US-EAST-1", "us-east", "us_east_1",
		"us-east-1 ", "us-east-1a", "-us-east-1", "us--east-1",
	}
	for _, name := range invalid {
		if err := CheckRegion(name); err == nil {
			t.Errorf("CheckRegion(%q) = nil, want an error", name)
		}
	}
}
