package main

import (
	"os"
	"path/filepath"
	"testing"

	rcs "github.com/arran4/golang-rcs"
)

func TestLoadFilePreservesCarriageReturnsInDeltaText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "binary,v")
	data := "" +
		"head\t1.2;\n" +
		"access;\n" +
		"symbols;\n" +
		"locks; strict;\n" +
		"comment\t@# @;\n" +
		"\n" +
		"\n" +
		"1.2\n" +
		"date\t2020.01.02.03.04.05;\tauthor u;\tstate Exp;\n" +
		"branches;\n" +
		"next\t1.1;\n" +
		"\n" +
		"1.1\n" +
		"date\t2020.01.02.03.04.04;\tauthor u;\tstate Exp;\n" +
		"branches;\n" +
		"next\t;\n" +
		"\n" +
		"\n" +
		"desc\n" +
		"@@\n" +
		"\n" +
		"\n" +
		"1.2\n" +
		"log\n" +
		"@new\n" +
		"@\n" +
		"text\n" +
		"@base\n" +
		"@\n" +
		"\n" +
		"\n" +
		"1.1\n" +
		"log\n" +
		"@old\n" +
		"@\n" +
		"text\n" +
		"@d1 1\n" +
		"a1 1\n" +
		"old\rwithcr\n" +
		"@\n"
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
	file, _, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	contents := make(map[string]*rcs.RevisionContent, len(file.RevisionContents))
	for _, rc := range file.RevisionContents {
		contents[rc.Revision] = rc
	}
	fulltext, err := reconstructTexts(file, contents)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fulltext["1.1"], "old\rwithcr\n"; got != want {
		t.Fatalf("fulltext[1.1] = %q, want %q", got, want)
	}
}
