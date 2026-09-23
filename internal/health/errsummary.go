package health

import (
	"errors"
	"fmt"
	"strings"

	"github.com/aws/smithy-go"
)

// errSummary renders err on one line for a health result message.
// awserr.FormatAWSError adds multi-line remediation text that would break the
// results table, so an API error shows its code and message, and any other
// error shows its first line.
func errSummary(err error) string {
	if err == nil {
		return ""
	}
	var ae smithy.APIError
	if errors.As(err, &ae) {
		return fmt.Sprintf("%s: %s", ae.ErrorCode(), ae.ErrorMessage())
	}
	msg, _, _ := strings.Cut(err.Error(), "\n")
	return strings.TrimSpace(msg)
}
