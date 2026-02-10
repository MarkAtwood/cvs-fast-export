// SPDX-License-Identifier: GPL-2.0+
package compare

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
)

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
		// extend to match longer buffer where possible
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
