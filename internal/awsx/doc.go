// Package awsx is how refresh reaches AWS: it loads the SDK configuration,
// builds the service clients for a region, sets the retry policy of each
// call, and classifies and explains the errors that come back.
//
// awsx imports no other refresh package.
package awsx
