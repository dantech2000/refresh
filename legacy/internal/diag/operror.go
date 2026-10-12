package diag

import "errors"

// opError is an error tagged with the IAM action that failed.
type opError struct {
	op  string
	err error
}

func (e *opError) Error() string { return e.err.Error() }
func (e *opError) Unwrap() error { return e.err }

// WithOperation tags err with op, the IAM action that failed, such as
// OpListNodegroups. Use it in a service method that makes several AWS calls,
// so the caller can build the Failure without knowing which call failed:
// FromError reads the tag when its op argument is "". The error text does not
// change, and errors.Is and errors.As see through the tag. A nil err stays
// nil.
func WithOperation(op string, err error) error {
	if err == nil {
		return nil
	}
	return &opError{op: op, err: err}
}

// OperationOf returns the IAM action err was tagged with by WithOperation,
// or "" when it has no tag. The outermost tag wins.
func OperationOf(err error) string {
	var oe *opError
	if errors.As(err, &oe) {
		return oe.op
	}
	return ""
}
