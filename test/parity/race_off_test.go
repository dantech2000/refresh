//go:build unix && !race

package parity

// raceBuild is true when the test binary was built with -race. TestMain then
// builds the binary under test with -race too.
const raceBuild = false
