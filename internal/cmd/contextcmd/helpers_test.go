package contextcmd_test

import (
	"io"
	"os"
	"testing"
)

func newPipe(t *testing.T) (*io.PipeReader, *io.PipeWriter) {
	t.Helper()
	r, w := io.Pipe()
	t.Cleanup(func() { _ = w.Close() })
	return r, w
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
