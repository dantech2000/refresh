package awsx

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
)

// regionPattern is the shape of an AWS region name: a partition prefix of
// two to four letters (us, eu, ap, cn, eusc, ...), one or more words
// (gov, iso, west, central, ...), and a number. It is a shape, not a list:
// AWS adds regions, and a list would refuse a new one.
var regionPattern = regexp.MustCompile(`^[a-z]{2,4}(-[a-z]+)+-[0-9]+$`)

// CheckRegion returns an error when name does not have the shape of an AWS
// region name, such as us-east-1, us-gov-west-1, or cn-north-1. It makes no
// call: a name of the right shape may still be a region that does not
// exist. The error names the value; the caller adds where it came from.
func CheckRegion(name string) error {
	if regionPattern.MatchString(name) {
		return nil
	}
	return fmt.Errorf("%q is not a region name: a region name is a partition prefix, a geographic word, and a number, such as us-east-1, us-gov-west-1, or cn-north-1", name)
}

// UnknownEndpoint returns the host of the AWS endpoint that err failed to
// find, when err is a DNS lookup of an AWS host that found no such host. A
// region name of the right shape that is not a region, such as us-east-9,
// fails this way: AWS has no endpoint for it.
func UnknownEndpoint(err error) (host string, ok bool) {
	var dns *net.DNSError
	if !errors.As(err, &dns) || !dns.IsNotFound || !isAWSHost(dns.Name) {
		return "", false
	}
	return strings.TrimSuffix(strings.ToLower(dns.Name), "."), true
}

// UnknownRegionMessage says that EKS does not answer in a region because
// its endpoint host does not resolve. It does not blame the account: an
// opt-in region that is not enabled still resolves.
func UnknownRegionMessage(host string) string {
	return "not a region where EKS answers: " + host + " does not resolve; check the region name"
}
