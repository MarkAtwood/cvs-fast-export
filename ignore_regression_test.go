package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExportIgnoreRules(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	native := "# native rules\r\n*.tmp\r\n!keep.tmp\r\nname with spaces\r\n"
	for _, tc := range []struct {
		name     string
		masters  map[string]string
		verbatim map[string]string
		ignored  []string
		visible  []string
	}{
		{
			name:    "root reset",
			masters: map[string]string{".cvsignore": "!\nkeep.tmp"},
			ignored: []string{"keep.tmp", "sub/file.o"},
			visible: []string{"file.o", "sub/keep.tmp"},
		},
		{
			name:    "bare reset",
			masters: map[string]string{".cvsignore": "!"},
			ignored: []string{"sub/file.o"}, visible: []string{"file.o"},
		},
		{
			name:    "reset after patterns and whitespace",
			masters: map[string]string{".cvsignore": "old.tmp\t!\r\nkeep.tmp\v#literal\f!literal"},
			ignored: []string{"keep.tmp", "#literal", "!literal"},
			visible: []string{"old.tmp", "file.o", "sub/keep.tmp"},
		},
		{
			name:    "nested patterns",
			masters: map[string]string{"sub/.cvsignore": "alpha beta\t#literal !literal\n"},
			ignored: []string{"sub/alpha", "sub/beta", "sub/#literal", "sub/!literal", "sub/file.o"},
			visible: []string{"alpha", "sub/deep/alpha"},
		},
		{
			name:    "nested reset",
			masters: map[string]string{"sub/.cvsignore": "old.tmp ! keep.tmp"},
			ignored: []string{"file.o", "sub/keep.tmp", "sub/deep/file.o"},
			visible: []string{"sub/file.o", "sub/old.tmp", "sub/deep/keep.tmp"},
		},
		{
			name:     "native gitignore files",
			masters:  map[string]string{".gitignore": native, "sub/.gitignore": "# nested\n!keep.tmp\n"},
			verbatim: map[string]string{".gitignore": native, "sub/.gitignore": "# nested\n!keep.tmp\n"},
			ignored:  []string{"other.tmp", "name with spaces"},
			visible:  []string{"keep.tmp", "sub/keep.tmp"},
		},
		{
			name:     "similar filename",
			masters:  map[string]string{"project.cvsignore": "leave this unchanged\n"},
			verbatim: map[string]string{"project.cvsignore": "leave this unchanged\n"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := t.TempDir()
			// Keep a root-level file so directory stripping preserves nested paths.
			tc.masters["README"] = "tracked\n"
			for name, content := range tc.masters {
				archive := fmt.Sprintf(`head 1.1;
access;
symbols;
locks; strict;


1.1
date 2020.01.02.03.04.05; author u; state Exp;
branches;
next ;


desc
@@


1.1
log
@initial
@
text
@%s@
`, strings.ReplaceAll(content, "@", "@@"))
				file := filepath.Join(source, name+",v")
				if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, []byte(archive), 0644); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command(os.Args[0], "-test.run=TestCLIHelper", "--", source)
			cmd.Env = append(os.Environ(), "CFE_CLI_HELPER=1")
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			stream, err := cmd.Output()
			if err != nil {
				t.Fatalf("export: %v\n%s", err, stderr.String())
			}
			repository := newHistoryGitRepo(t, git)
			importHistoryStream(t, git, repository, string(stream))
			runHistoryGit(t, git, "", "-C", repository, "checkout", "--quiet", "master")
			for file, want := range tc.verbatim {
				if got := runHistoryGit(t, git, "", "-C", repository, "show", "master:"+file); got != want {
					t.Errorf("%s = %q, want original bytes %q", file, got, want)
				}
			}
			for ignored, files := range map[bool][]string{true: tc.ignored, false: tc.visible} {
				for _, file := range files {
					cmd := exec.Command(git, "-C", repository, "-c", "core.excludesFile=/dev/null",
						"check-ignore", "--no-index", "--quiet", "--", file)
					output, err := cmd.CombinedOutput()
					var exitErr *exec.ExitError
					if err != nil && (!errors.As(err, &exitErr) || exitErr.ExitCode() != 1) {
						t.Fatalf("check-ignore: %v\n%s", err, output)
					}
					if got := err == nil; got != ignored {
						t.Errorf("%s ignored = %t, want %t", file, got, ignored)
					}
				}
			}
		})
	}
}
