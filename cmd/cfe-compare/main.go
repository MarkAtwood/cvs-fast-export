// SPDX-License-Identifier: GPL-2.0+
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"thyrsus.com/cvs-fast-export/internal/compare"
)

// stringList collects repeatable flag values.
type stringList []string

// String formats the list for flag display.
func (s *stringList) String() string { return strings.Join(*s, ",") }
// Set appends a new value to the list.
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// main compares C and Go fast-export streams for arbitrary inputs.
func main() {
	var cBin string
	var goBin string
	var args stringList
	var roots stringList
	var files stringList
	var stdinList string

	flag.StringVar(&cBin, "c-bin", "", "path to C cvs-fast-export binary")
	flag.StringVar(&goBin, "go-bin", "", "path to Go cvs-fast-export binary")
	flag.Var(&args, "arg", "argument passed to both binaries (repeatable)")
	flag.Var(&roots, "root", "root directory to scan for ',v' files (repeatable)")
	flag.Var(&files, "file", "explicit file to include (repeatable)")
	flag.StringVar(&stdinList, "stdin-list", "", "file containing input paths, one per line")
	flag.Parse()

	if cBin == "" || goBin == "" {
		fmt.Fprintln(os.Stderr, "cfe-compare: --c-bin and --go-bin are required")
		os.Exit(1)
	}

	inputs, err := collectInputs(roots, files, stdinList)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cfe-compare: %v\n", err)
		os.Exit(1)
	}

	res, err := compare.Run(cBin, goBin, args, inputs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cfe-compare: %v\n", err)
		os.Exit(1)
	}

	if res.Match {
		fmt.Printf("OK (%d bytes)\n", len(res.CStdout))
		os.Exit(0)
	}

	fmt.Fprintf(os.Stderr, "Mismatch: C exit=%d Go exit=%d\n", res.CExit, res.GoExit)
	if res.DiffOffset >= 0 {
		fmt.Fprintf(os.Stderr, "%s", res.Context)
	}
	if len(res.CStderr) > 0 {
		fmt.Fprintf(os.Stderr, "C stderr:\n%s\n", strings.TrimSpace(string(res.CStderr)))
	}
	if len(res.GoStderr) > 0 {
		fmt.Fprintf(os.Stderr, "Go stderr:\n%s\n", strings.TrimSpace(string(res.GoStderr)))
	}
	os.Exit(1)
}

// collectInputs enumerates input RCS files from roots, files, and lists.
func collectInputs(roots, files []string, stdinList string) ([]string, error) {
	var inputs []string

	if stdinList != "" {
		data, err := os.ReadFile(stdinList)
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			inputs = append(inputs, line)
		}
	}

	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if strings.HasSuffix(path, ",v") {
				inputs = append(inputs, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	inputs = append(inputs, files...)

	uniq := make(map[string]struct{}, len(inputs))
	out := make([]string, 0, len(inputs))
	for _, in := range inputs {
		if _, ok := uniq[in]; ok {
			continue
		}
		uniq[in] = struct{}{}
		out = append(out, in)
	}

	sort.Slice(out, func(i, j int) bool {
		return compare.PathDeepCompare(out[i], out[j]) < 0
	})
	return out, nil
}
