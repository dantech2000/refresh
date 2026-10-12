package ui

import (
	"bytes"
	"testing"
)

func TestPlainTable(t *testing.T) {
	var buf bytes.Buffer
	NewPlainTable("NAME", "STATUS", "NOTE").
		Row("a\tb", "\x1b[32mACTIVE\x1b[0m", "line one\nline two").
		Row("empty", "", "  ").
		Row("dropped"). // wrong cell count: dropped, not shifted
		Write(&buf)
	want := "NAME\tSTATUS\tNOTE\n" +
		"a b\tACTIVE\tline one line two\n" +
		"empty\t-\t-\n"
	if got := buf.String(); got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestPlainKV(t *testing.T) {
	var buf bytes.Buffer
	NewPlainKV().Add("status", "ACTIVE").Write(&buf)
	if got, want := buf.String(), "FIELD\tVALUE\nstatus\tACTIVE\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
