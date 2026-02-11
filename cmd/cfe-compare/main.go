// SPDX-License-Identifier: GPL-2.0+
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
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

	res, err := Run(cBin, goBin, args, inputs)
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
		return PathDeepCompare(out[i], out[j]) < 0
	})
	return out, nil
}

// PathDeepCompare orders paths so deeper entries sort before their parents,
// matching git fast-export's deletion-safe ordering.
func PathDeepCompare(a, b string) int {
	if a == b {
		return 0
	}
	compar := strings.Compare(a, b)
	if compar == 0 {
		return 0
	}

	aslash := strings.LastIndexByte(a, '/')
	bslash := strings.LastIndexByte(b, '/')

	if aslash == -1 && bslash == -1 {
		return compar
	}
	if aslash == -1 {
		return 1
	}
	if bslash == -1 {
		return -1
	}

	alen := len(a)
	blen := len(b)

	if aslash == -1 || bslash == -1 {
		return compar
	}

	if aslash < bslash {
		aidx := aslash
		if aidx < blen && b[aidx] == '/' && strings.HasPrefix(a, b[:aidx]) {
			return 1
		}
	} else {
		bidx := bslash
		if bidx < alen && a[bidx] == '/' && strings.HasPrefix(b, a[:bidx]) {
			return -1
		}
	}
	return compar
}

// Result captures paired execution output for C vs Go comparisons.
type Result struct {
	Match      bool
	CExit      int
	GoExit     int
	DiffOffset int
	Context    string
	CStdout    []byte
	GoStdout   []byte
	CStderr    []byte
	GoStderr   []byte
}

// Run executes both binaries with identical stdin and compares outputs.
func Run(cBin, goBin string, args []string, inputs []string) (*Result, error) {
	stdin := []byte(joinLines(inputs))
	cOut, cErr, cExit, err := runOnce(cBin, args, stdin)
	if err != nil {
		return nil, err
	}
	goOut, goErr, goExit, err := runOnce(goBin, args, stdin)
	if err != nil {
		return nil, err
	}

	res := &Result{
		CExit:    cExit,
		GoExit:   goExit,
		CStdout:  append([]byte(nil), cOut...),
		GoStdout: append([]byte(nil), goOut...),
		CStderr:  append([]byte(nil), cErr...),
		GoStderr: append([]byte(nil), goErr...),
	}

	if cExit != goExit {
		res.Match = false
		res.DiffOffset = -1
		return res, nil
	}
	if bytes.Equal(cOut, goOut) {
		res.Match = true
		res.DiffOffset = -1
		return res, nil
	}

	off := firstDiffOffset(cOut, goOut)
	res.DiffOffset = off
	res.Context = diffContext(cOut, goOut, off)
	return res, nil
}

// joinLines builds a newline-terminated stdin payload from path lines.
func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	var b bytes.Buffer
	for _, line := range lines {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// runOnce executes a command with provided stdin and captures output and exit code.
func runOnce(bin string, args []string, stdin []byte) ([]byte, []byte, int, error) {
	cmd := exec.Command(bin, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err == nil {
		return outBuf.Bytes(), errBuf.Bytes(), 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return outBuf.Bytes(), errBuf.Bytes(), exitErr.ExitCode(), nil
	}
	return nil, nil, 0, fmt.Errorf("run %s: %w", bin, err)
}

// firstDiffOffset returns the first differing byte offset or -1 if equal.
func firstDiffOffset(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	if len(a) != len(b) {
		return n
	}
	return -1
}

// diffContext provides a small hex window around the first mismatch.
func diffContext(a, b []byte, off int) string {
	if off < 0 {
		return ""
	}
	const window = 64
	start := off - window
	if start < 0 {
		start = 0
	}
	end := off + window
	if end > len(a) {
		end = len(a)
	}
	if end < len(b) {
		if off+window <= len(b) {
			end = off + window
		} else {
			end = len(b)
		}
	}
	aSeg := a[start:end]
	bSeg := b[start:end]
	return fmt.Sprintf("offset %d\nC: %s\nG: %s\n", off, hexDump(aSeg), hexDump(bSeg))
}

// hexDump renders bytes as compact hex for diff context.
func hexDump(b []byte) string {
	if len(b) == 0 {
		return "(empty)"
	}
	var buf bytes.Buffer
	for i, v := range b {
		if i > 0 && i%16 == 0 {
			buf.WriteString(" ")
		}
		fmt.Fprintf(&buf, "%02x", v)
	}
	return buf.String()
}
