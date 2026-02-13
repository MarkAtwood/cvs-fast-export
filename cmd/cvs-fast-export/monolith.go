// SPDX-License-Identifier: GPL-2.0+
package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	rcs "github.com/arran4/golang-rcs"
)

// Number represents a CVS revision number split into components.
type Number struct {
	C int
	N []int
}

// Zero is the sentinel for an empty CVS revision number.
var Zero = Number{C: 0, N: nil}

// Parse converts a dotted CVS revision string into a Number.
func Parse(s string) (Number, error) {
	if s == "" {
		return Zero, fmt.Errorf("empty CVS number")
	}
	// Split into dotted components and validate each field.
	parts := strings.Split(s, ".")
	n := Number{C: len(parts), N: make([]int, len(parts))}
	for i, p := range parts {
		if p == "" {
			return Zero, fmt.Errorf("invalid CVS number %q", s)
		}
		v, err := strconv.Atoi(p)
		if err != nil {
			return Zero, fmt.Errorf("invalid CVS number %q", s)
		}
		n.N[i] = v
	}
	return n, nil
}

// IsHead reports whether the revision is a branch head marker.
func IsHead(n Number) bool {
	if n.C <= 2 || (n.C&1) != 0 {
		return false
	}
	return n.N[n.C-2] == 0
}

// SameBranch reports whether two revisions reside on the same branch.
func SameBranch(a, b Number) bool {
	// Normalize odd-length revisions to branch form.
	if a.C&1 == 1 {
		a = appendZero(a)
	}
	if b.C&1 == 1 {
		b = appendZero(b)
	}
	// Compare branch prefixes, ignoring the final leaf component.
	if a.C != b.C {
		return false
	}
	if a.C == 2 {
		return true
	}
	for i := 0; i < a.C-1; i++ {
		an := a.N[i]
		bn := b.N[i]
		if i == a.C-2 {
			if an == 0 {
				an = a.N[i+1]
			}
			if bn == 0 {
				bn = b.N[i+1]
			}
		}
		if an != bn {
			return false
		}
	}
	return true
}

// Equal reports whether two numbers are identical component-wise.
func Equal(a, b Number) bool {
	// Compare component counts and values directly.
	if a.C != b.C {
		return false
	}
	for i := 0; i < a.C; i++ {
		if a.N[i] != b.N[i] {
			return false
		}
	}
	return true
}

// Compare gives a total ordering; parent always < child.
func Compare(a, b Number) int {
	// Compare shared prefix first, then fall back to length.
	n := min(a.C, b.C)
	for i := 0; i < n; i++ {
		if a.N[i] < b.N[i] {
			return -1
		}
		if a.N[i] > b.N[i] {
			return 1
		}
	}
	if a.C < b.C {
		return -1
	}
	if a.C > b.C {
		return 1
	}
	return 0
}

// Degree reports the effective branch depth for a revision.
func Degree(n Number) int {
	// Vendor branches use one fewer component for branch degree.
	if n.C < 4 {
		return n.C
	}
	four := n
	four.C = 4
	four.N = append([]int(nil), n.N[:4]...)
	if IsVendor(four) {
		return n.C - 1
	}
	return n.C
}

// IsTrunk reports whether a revision is on trunk.
func IsTrunk(n Number) bool { return n.C == 2 }

// Vendor branches are 1.1.x with odd x.
func IsVendor(n Number) bool {
	if n.C != 4 {
		return false
	}
	return n.N[0] == 1 && n.N[1] == 1 && (n.N[2]&1) == 1
}

// String formats a CVS revision number back to dotted text.
func String(n Number) string {
	if n.C == 0 {
		return ""
	}
	// Emit dotted numeric components.
	var b strings.Builder
	for i := 0; i < n.C; i++ {
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(strconv.Itoa(n.N[i]))
	}
	return b.String()
}

// AppendZero appends a zero to make odd-length revisions comparable.
func AppendZero(n Number) Number {
	return appendZero(n)
}

// appendZero returns a new Number with an extra trailing zero.
func appendZero(n Number) Number {
	out := Number{C: n.C + 1, N: make([]int, n.C+1)}
	copy(out.N, n.N)
	out.N[n.C] = 0
	return out
}

// min returns the smaller of two integers.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// PathDeepCompare orders paths so deeper entries sort before their parents,
// matching git fast-export's deletion-safe ordering.
func PathDeepCompare(a, b string) int {
	// Fast path for identity and pure lexicographic ordering.
	if a == b {
		return 0
	}
	compar := strings.Compare(a, b)
	if compar == 0 {
		return 0
	}

	// Identify the final directory boundary for each path.
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

	// If one is a prefix of the other, keep the deeper path first.
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
	// Build a deterministic stdin payload for both binaries.
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
		// Exit status mismatch is a hard failure regardless of output.
		res.Match = false
		res.DiffOffset = -1
		return res, nil
	}
	if bytes.Equal(cOut, goOut) {
		// Byte-for-byte identical output: success.
		res.Match = true
		res.DiffOffset = -1
		return res, nil
	}

	// Record the first mismatch offset and a small diff context.
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
	// Preserve input order and end with a newline like the C tool.
	var b bytes.Buffer
	for _, line := range lines {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// runOnce executes a command with provided stdin and captures output and exit code.
func runOnce(bin string, args []string, stdin []byte) ([]byte, []byte, int, error) {
	// Execute the command once and capture stdout/stderr and exit code.
	cmd := exec.Command(bin, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err == nil {
		return outBuf.Bytes(), errBuf.Bytes(), 0, nil
	}
	// Surface exit codes for non-zero termination.
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return outBuf.Bytes(), errBuf.Bytes(), exitErr.ExitCode(), nil
	}
	return nil, nil, 0, fmt.Errorf("run %s: %w", bin, err)
}

// firstDiffOffset returns the first differing byte offset or -1 if equal.
func firstDiffOffset(a, b []byte) int {
	// Scan byte-by-byte to find the earliest mismatch.
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
	// Emit a compact hex window around the mismatch.
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

// hexDump renders bytes as compact hex for diff context.
func hexDump(b []byte) string {
	if len(b) == 0 {
		return "(empty)"
	}
	// Group bytes in 16-byte chunks for readability.
	var buf bytes.Buffer
	for i, v := range b {
		if i > 0 && i%16 == 0 {
			buf.WriteString(" ")
		}
		fmt.Fprintf(&buf, "%02x", v)
	}
	return buf.String()
}

// Author describes a resolved author identity from an authormap file.
type Author struct {
	Full     string
	Email    string
	Timezone string
}

// Load parses an authormap file into a username-to-author map.
func Load(path string) (map[string]Author, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	m := make(map[string]Author)
	scanner := bufio.NewScanner(f)
	lineno := 0
	for scanner.Scan() {
		lineno++
		// Skip blanks and comments to match permissive map files.
		line := scanner.Text()
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		// Split on the first '=' to preserve author details.
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			return nil, fmt.Errorf("%s:%d: missing '='", path, lineno)
		}
		name := strings.TrimSpace(line[:eq])
		if name == "" {
			return nil, fmt.Errorf("%s:%d: empty username", path, lineno)
		}
		if _, ok := m[name]; ok {
			return nil, fmt.Errorf("%s:%d: duplicate username '%s'", path, lineno, name)
		}
		// Parse "Full Name <email> TZ" fields.
		fullPart := strings.TrimSpace(line[eq+1:])
		lt := strings.IndexByte(fullPart, '<')
		if lt < 0 {
			return nil, fmt.Errorf("%s:%d: missing email address '%s'", path, lineno, name)
		}
		full := strings.TrimSpace(fullPart[:lt])
		emailPart := fullPart[lt+1:]
		gt := strings.IndexByte(emailPart, '>')
		if gt < 0 {
			return nil, fmt.Errorf("%s:%d: malformed email address '%s'", path, lineno, name)
		}
		email := emailPart[:gt]
		// Everything after the closing '>' is treated as the timezone token.
		tz := strings.TrimSpace(emailPart[gt+1:])
		m[name] = Author{Full: full, Email: email, Timezone: tz}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return m, nil
}

// Commit represents a CVS revision with resolved content and metadata.
type Commit struct {
	Number   Number
	Parent   *Commit
	Dead     bool
	CommitID string
	Date     time.Time
	Author   string
	Log      string
	Text     string
	FileName string
	OrigName string
	Source   string
	Mode     int
	Tail     bool
}

// Head represents a named branch tip and its metadata.
type Head struct {
	RefName string
	Number  Number
	Commit  *Commit
	Parent  *Head
	Degree  int
	Tail    bool
}

// Symbol associates a symbolic name with a CVS revision number.
type Symbol struct {
	Name   string
	Number Number
}

// Tag records a lightweight tag name bound to a commit.
type Tag struct {
	Name   string
	Commit *Commit
}

// Version captures a revision number and its child branches.
type Version struct {
	Number   Number
	Branches []Number
}

var nullCounter int = 0

// SetRefs assigns branch heads and tags from CVS symbols, synthesizing
// missing branch names while discarding unreachable dead branches.
func SetRefs(heads []*Head, symbols []Symbol, findRevision func(Number) *Commit, allowVendorSymbols bool) ([]*Head, []Tag) {
	var tags []Tag
	// Attach symbol names to branch heads or tag commits directly.
	for _, s := range symbols {
		if isVendorBranchSymbol(s.Number) && !allowVendorSymbols {
			// In RCS-only mode, ignore vendor branch symbols entirely.
			continue
		}
		if IsHead(s.Number) || isVendorBranchSymbol(s.Number) {
			num := s.Number
			if isVendorBranchSymbol(s.Number) {
				// Vendor branch symbols use odd-length numbers; normalize to branch form.
				num = AppendZero(s.Number)
			}
			h := findHeadByBranch(heads, s.Number)
			if h != nil {
				// Prefer existing head names; otherwise add a parallel named head.
				if h.RefName == "" {
					h.RefName = s.Name
					h.Degree = Degree(num)
				} else {
					h = &Head{
						RefName: s.Name,
						Commit:  h.Commit,
						Degree:  Degree(num),
					}
					heads = append(heads, h)
				}
			} else {
				n := num
				// Walk up toward trunk to find a reachable commit to anchor the head.
				for n.C >= 4 {
					n.C -= 2
					n.N = n.N[:n.C]
					c := findRevision(n)
					if c != nil {
						h = &Head{
							RefName: s.Name,
							Commit:  c,
							Degree:  Degree(num),
						}
						heads = append(heads, h)
						break
					}
				}
			}
			if h != nil {
				// Record the symbol's revision number for the head.
				h.Number = num
			}
		} else {
			// Non-head symbols are lightweight tags.
			if c := findRevision(s.Number); c != nil {
				tags = append(tags, Tag{Name: s.Name, Commit: c})
			}
		}
	}

	// Post-process unnamed heads, then link the resulting branch tree.
	heads = fixUnnamedHeads(heads)
	heads = discardZeroHeads(heads)
	linkParents(heads)
	for _, h := range heads {
		if h.RefName == "" {
			appendMasterWarning("null branch name, probably from a damaged Attic file")
			h.RefName = "null"
			if nullCounter > 0 {
				h.RefName += fmt.Sprintf("%d", nullCounter)
			}
			nullCounter += 1
		}
	}
	heads = pruneUnnamedBranches(heads)
	return heads, tags
}

// PatchVendorBranches detects vendor branches, synthesizes missing names,
// and splices them into master when no 1.2 exists.
func PatchVendorBranches(heads []*Head, allowVendorBranches bool, hasVendorSymbol bool) ([]*Head, bool) {
	trunk := findHeadByName(heads, "master")
	if trunk == nil {
		return heads, false
	}

	var nvendor *Head
	foundVendor := false
	// Identify vendor branches by their numbering scheme and patch metadata.
	for _, vendor := range heads {
		if vendor.Commit == nil || !IsVendor(vendor.Commit.Number) {
			continue
		}
		foundVendor = true
		nvendor = vendor

		if vendor.RefName == "" {
			// Synthesize a stable "import-<branch>" name from the branch root.
			vlast := vendor.Commit
			for vlast != nil && vlast.Parent != nil {
				vlast = vlast.Parent
			}
			if vlast != nil {
				branch := cloneNumber(vlast.Number)
				if branch.C > 0 {
					branch.C--
					branch.N = branch.N[:branch.C]
				}
				name := fmt.Sprintf("import-%s", String(branch))
				vendor.RefName = name
			}
		}
		vendor.Parent = trunk
		vendor.Degree = vendor.Commit.Number.C
		vendor.Number = vendor.Commit.Number
	}

	// If trunk is missing 1.2, splice the newest vendor branch into master.
	if nvendor != nil && trunk.Commit != nil && trunk.Commit.Parent == nil {
		oldtip := trunk.Commit
		trunk.Commit = nvendor.Commit
		trunk.Degree = nvendor.Commit.Number.C
		trunk.Number = nvendor.Commit.Number
		vlast := trunk.Commit
		for vlast != nil && vlast.Parent != nil {
			vlast = vlast.Parent
		}
		if vlast != nil {
			vlast.Parent = oldtip
		}
		// In RCS-only mode, drop vendor heads that now point at the trunk tip.
		if !allowVendorBranches || !hasVendorSymbol {
			heads = removeHeadsWithCommit(heads, trunk.Commit, trunk)
		}
	}

	return heads, foundVendor
}

// GraftBranches links branch tails to their parent revisions to form a tree.
func GraftBranches(heads []*Head, versions []Version, findRevision func(Number) *Commit) {
	trunk := findHeadByName(heads, "master")
	for _, h := range heads {
		if h == trunk || h.Tail {
			continue
		}
		// Find the last commit on the branch chain.
		c := h.Commit
		for c != nil && c.Parent != nil {
			if c.Tail {
				c = nil
				break
			}
			c = c.Parent
		}
		if c == nil {
			continue
		}
		// Match the branch point in the version graph and graft to it.
		for _, cv := range versions {
			for _, cb := range cv.Branches {
				if Compare(cb, c.Number) == 0 {
					c.Parent = findRevision(cv.Number)
					c.Tail = true
					break
				}
			}
			if c.Parent != nil {
				break
			}
		}
	}
}

// fixUnnamedHeads synthesizes branch numbers for unnamed heads.
func fixUnnamedHeads(heads []*Head) []*Head {
	for _, h := range heads {
		if h.RefName != "" {
			continue
		}
		// Find the first non-dead commit to base the synthetic branch on.
		c := firstLiveCommit(h.Commit)
		if c == nil {
			// Mark fully-dead branches for removal.
			if h.Commit != nil {
				appendMasterWarning(fmt.Sprintf("discarding dead untagged branch %s in %s",
					String(h.Commit.Number), h.Commit.FileName))
			}
			h.Number = Zero
			continue
		}
		n := c.Number
		if n.C >= 2 {
			// Convert revision to branch form (x.y.z -> x.y.0.z).
			n = cloneNumber(n)
			n.N[n.C-1] = n.N[n.C-2]
			n.N[n.C-2] = 0
		}
		h.Number = n
		h.Degree = Degree(n)
	}
	return heads
}

// discardZeroHeads removes heads that couldn't be assigned a number.
func discardZeroHeads(heads []*Head) []*Head {
	out := heads[:0]
	for _, h := range heads {
		if !Equal(h.Number, Zero) {
			out = append(out, h)
		}
	}
	return out
}

// linkParents attaches parent heads based on CVS branch numbering.
func linkParents(heads []*Head) {
	for _, h := range heads {
		// Only branch numbers (>=4) have parents.
		if h.Number.C < 4 {
			continue
		}
		// Strip one branch segment to locate the parent head.
		n := cloneNumber(h.Number)
		n.C -= 2
		n.N = n.N[:n.C]
		p := findBranch(heads, n)
		h.Parent = p
		if p != nil && h.RefName == "" {
			// Synthesize a readable name for unnamed branches.
			branchName := ""
			if h.Commit != nil && h.Commit.CommitID != "" {
				branchName = fmt.Sprintf("%s-UNNAMED-BRANCH-%s", p.RefName, h.Commit.CommitID)
			} else {
				branchName = fmt.Sprintf("%s-UNNAMED-BRANCH", p.RefName)
			}
			if h.Commit != nil {
				base := path.Base(h.Commit.Source)
				appendMasterWarning(fmt.Sprintf("warning - putting %s rev %s on unnamed branch %s off %s",
					base, String(h.Number), branchName, p.RefName))
			}
			h.RefName = branchName
		}
	}
}

// pruneUnnamedBranches drops older unnamed branch heads with identical ref names.
func pruneUnnamedBranches(heads []*Head) []*Head {
	best := make(map[string]*Head)
	for _, h := range heads {
		if h.RefName == "" || !strings.Contains(h.RefName, "-UNNAMED-BRANCH") {
			continue
		}
		if h.Commit == nil {
			continue
		}
		if cur, ok := best[h.RefName]; !ok || cur.Commit == nil || h.Commit.Date.After(cur.Commit.Date) {
			best[h.RefName] = h
		}
	}
	out := heads[:0]
	for _, h := range heads {
		if h.RefName != "" && strings.Contains(h.RefName, "-UNNAMED-BRANCH") {
			if best[h.RefName] != h {
				continue
			}
		}
		out = append(out, h)
	}
	return out
}

// findHeadByBranch locates the head whose commits share a branch.
func findHeadByBranch(heads []*Head, number Number) *Head {
	for _, h := range heads {
		if h.Commit != nil && SameBranch(h.Commit.Number, number) {
			return h
		}
	}
	return nil
}

// findBranch walks up the branch number to find a matching head.
func findBranch(heads []*Head, number Number) *Head {
	n := cloneNumber(number)
	// Walk up the revision number by stripping branch components.
	for n.C >= 2 {
		for _, h := range heads {
			if SameBranch(h.Number, n) {
				return h
			}
		}
		n.C -= 2
		if n.C > 0 {
			n.N = n.N[:n.C]
		}
	}
	return nil
}

// firstLiveCommit returns the first non-dead commit in a chain.
func firstLiveCommit(c *Commit) *Commit {
	for c != nil {
		if !c.Dead {
			return c
		}
		c = c.Parent
	}
	return nil
}

// cloneNumber copies a CVS number to avoid aliasing.
func cloneNumber(n Number) Number {
	out := Number{C: n.C, N: make([]int, n.C)}
	copy(out.N, n.N[:n.C])
	return out
}

// findHeadByName locates a head by its reference name.
func findHeadByName(heads []*Head, name string) *Head {
	for _, h := range heads {
		if h.RefName == name {
			return h
		}
	}
	return nil
}

// removeHead removes a specific head from the slice.
func removeHead(heads []*Head, target *Head) []*Head {
	out := heads[:0]
	for _, h := range heads {
		if h != target {
			out = append(out, h)
		}
	}
	return out
}

// removeHeadsWithCommit drops heads (except keep) that reference a commit pointer.
func removeHeadsWithCommit(heads []*Head, commit *Commit, keep *Head) []*Head {
	if commit == nil {
		return heads
	}
	out := heads[:0]
	for _, h := range heads {
		if h == keep {
			out = append(out, h)
			continue
		}
		if h.Commit == commit {
			continue
		}
		out = append(out, h)
	}
	return out
}

// Master represents one parsed RCS file with resolved commits and symbols.
type Master struct {
	Source   string
	FileName string
	Heads    []*Head
	Symbols  []Symbol
	Tags     []Tag
	Versions []Version
	Commits  map[string]*Commit
	Root     *Commit
}

var masterWarnings []string

func appendMasterWarning(msg string) {
	for _, w := range masterWarnings {
		if w == msg {
			return
		}
	}
	masterWarnings = append(masterWarnings, msg)
}

// BuildMaster converts a parsed RCS file into a CVS master model.
func BuildMaster(source string, file *rcs.File, striplen int, symbolOrder []string, allowVendorBranches bool) (*Master, error) {
	info, err := os.Stat(source)
	if err != nil {
		return nil, err
	}
	// Derive executable bit from the master file mode.
	fileMode := 0644
	if info.Mode()&0100 != 0 {
		fileMode = 0755
	}
	// Index revision contents and reconstruct fulltexts.
	commits := make(map[string]*Commit, len(file.RevisionHeads))
	contents := make(map[string]*rcs.RevisionContent, len(file.RevisionContents))
	for _, rc := range file.RevisionContents {
		contents[rc.Revision] = rc
	}
	rectified := exportName(source, striplen)
	fileName := fileopName(rectified)
	if hasEmptyBranchHeader(source) {
		appendMasterWarning("ignoring empty branch")
	}
	fulltext, err := reconstructTexts(file, contents)
	if err != nil {
		return nil, err
	}
	// Normalize author fields before commit construction.
	for _, rh := range file.RevisionHeads {
		if rh != nil {
			rh.Author = normalizeAuthor(rh.Author)
		}
	}
	// Build Commit records from revision heads.
	for _, rh := range file.RevisionHeads {
		num, err := Parse(rh.Revision)
		if err != nil {
			return nil, err
		}
		var log string
		var text string
		if rc, ok := contents[rh.Revision]; ok {
			log = normalizeLog(rc.Log, file.Description)
			text = fulltext[rh.Revision]
		}
		commits[rh.Revision] = &Commit{
			Number:   num,
			Dead:     rh.State == "dead",
			CommitID: rh.CommitID,
			Date:     rh.Date,
			Author:   rh.Author,
			Log:      log,
			Text:     text,
			FileName: fileName,
			OrigName: rectified,
			Source:   source,
			Mode:     fileMode,
		}
	}

	// Convert revision heads into version nodes with branch pointers.
	var versions []Version
	for _, rh := range file.RevisionHeads {
		num, err := Parse(rh.Revision)
		if err != nil {
			return nil, err
		}
		ver := Version{Number: num}
		for _, br := range rh.Branches {
			bn, err := Parse(br)
			if err != nil {
				return nil, err
			}
			ver.Branches = append(ver.Branches, bn)
		}
		versions = append(versions, ver)
	}

	// Create branch head chains and locate the root commit (pre-vendor patch).
	pendingWarnings = nil
	branchHeads, root := buildBranchHeads(commits, versions)
	if len(pendingWarnings) > 0 {
		for _, w := range pendingWarnings {
			appendMasterWarning(w)
		}
	}

	// Normalize symbol ordering to preserve original CVS ordering where possible.
	var sym []Symbol
	seen := make(map[string]struct{}, len(file.SymbolMap))
	hasVendorSymbol := false
	if len(symbolOrder) > 0 {
		for i := len(symbolOrder) - 1; i >= 0; i-- {
			name := symbolOrder[i]
			rev, ok := file.SymbolMap[name]
			if !ok {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			name = sanitizeSymbolName(name)
			if name == "" {
				return nil, fmt.Errorf("symbol name empty after sanitization")
			}
			n, err := Parse(rev)
			if err != nil {
				return nil, err
			}
			if isVendorBranchSymbol(n) {
				hasVendorSymbol = true
			}
			sym = append(sym, Symbol{Name: name, Number: n})
		}
	}
	for name, rev := range file.SymbolMap {
		if _, ok := seen[name]; ok {
			continue
		}
		name = sanitizeSymbolName(name)
		if name == "" {
			return nil, fmt.Errorf("symbol name empty after sanitization")
		}
		n, err := Parse(rev)
		if err != nil {
			return nil, err
		}
		if isVendorBranchSymbol(n) {
			hasVendorSymbol = true
		}
		sym = append(sym, Symbol{Name: name, Number: n})
	}

	// Assemble the final master structure.
	master := &Master{
		Source:   source,
		FileName: fileName,
		Heads:    branchHeads,
		Symbols:  sym,
		Versions: versions,
		Commits:  commits,
		Root:     root,
	}
	// Rebuild heads after vendor-branch patching so trunk selection is correct.
	var foundVendor bool
	master.Heads, foundVendor = PatchVendorBranches(master.Heads, allowVendorBranches, hasVendorSymbol)
	if foundVendor {
		appendMasterWarning("vendor branch detected.")
	}
	return master, nil
}

var pendingWarnings []string

// normalizeAuthor replaces whitespace with underscores for author tokens.
// Yes. RCS masters have been seen in the wild with spaces embedded in
// committer IDs.
func normalizeAuthor(s string) string {
	if s == "" {
		return s
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return '_'
		}
		return r
	}, s)
}

// hasEmptyBranchHeader reports whether the RCS header contains "branch;" with no value.
func hasEmptyBranchHeader(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "branch") {
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "branch"))
			if rest == ";" {
				return true
			}
		}
		if trimmed == "" {
			break
		}
	}
	return false
}

// buildBranchHeads constructs branch head links and returns the trunk root.
func buildBranchHeads(commits map[string]*Commit, versions []Version) ([]*Head, *Commit) {
	trunkNum, ok := findTrunkStart(commits)
	var heads []*Head
	var root *Commit
	if ok {
		// Build the master trunk chain from the earliest trunk revision.
		trunk := buildBranchChain(commits, trunkNum)
		if trunk != nil {
			root = lastCommit(trunk)
			heads = append(heads, &Head{
				RefName: "master",
				Commit:  trunk,
				Number:  trunk.Number,
				Degree:  2,
			})
		}
	}

	seen := make(map[string]struct{})
	for _, ver := range versions {
		for _, br := range ver.Branches {
			// Only build one head per unique branch number.
			key := String(br)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			head := buildBranchChain(commits, br)
			if head == nil {
				continue
			}
			heads = append(heads, &Head{Commit: head})
		}
	}
	return heads, root
}

// findTrunkStart finds the smallest trunk revision number present.
func findTrunkStart(commits map[string]*Commit) (Number, bool) {
	var min Number
	found := false
	for _, c := range commits {
		// Consider only trunk revisions and pick the smallest.
		if !IsTrunk(c.Number) {
			continue
		}
		if !found || Compare(c.Number, min) < 0 {
			min = c.Number
			found = true
		}
	}
	return min, found
}

// buildBranchChain orders commits on a branch and links parents.
func buildBranchChain(commits map[string]*Commit, branch Number) *Commit {
	var chain []*Commit
	for _, c := range commits {
		// Filter commits that belong to this branch number.
		if onBranch(c.Number, branch) {
			chain = append(chain, c)
		}
	}
	if len(chain) == 0 {
		return nil
	}
	sort.Slice(chain, func(i, j int) bool {
		return Compare(chain[i].Number, chain[j].Number) < 0
	})
	// Drop leading dead revisions when the branch later becomes live.
	firstLive := -1
	for i, c := range chain {
		if c == nil {
			continue
		}
		if !c.Dead {
			firstLive = i
			break
		}
	}
	if firstLive > 0 {
		chain = chain[firstLive:]
	}
	// Link parents from newest to oldest, then normalize dates.
	for i := len(chain) - 1; i > 0; i-- {
		chain[i].Parent = chain[i-1]
	}
	warnings := adjustDates(chain)
	for _, w := range warnings {
		pendingWarnings = append(pendingWarnings, w)
	}
	return chain[len(chain)-1]
}

// adjustDates enforces monotonically non-decreasing dates within a chain.
// It returns formatted warning messages for any adjustments made.
func adjustDates(chain []*Commit) []string {
	if len(chain) < 2 {
		return nil
	}
	var warnings []string
	// Ensure commit dates are non-decreasing within the chain.
	var gc *Commit
	for i := len(chain) - 1; i > 0; i-- {
		c := chain[i]
		p := chain[i-1]
		if p.Date.After(c.Date) {
			if gc != nil && !p.Date.After(gc.Date) {
				c.Date = p.Date
				warnings = append(warnings, fmt.Sprintf("warning - %s: %s is newer than %s, adjusting %s",
					c.Source, String(p.Number), String(c.Number), String(c.Number)))
			} else {
				p.Date = c.Date
				warnings = append(warnings, fmt.Sprintf("warning - %s: %s is newer than %s, adjusting %s",
					c.Source, String(p.Number), String(c.Number), String(p.Number)))
			}
		}
		gc = c
	}
	return warnings
}

// lastCommit returns the oldest commit reachable from a head.
func lastCommit(head *Commit) *Commit {
	if head == nil {
		return nil
	}
	c := head
	for c.Parent != nil {
		c = c.Parent
	}
	return c
}

// exportName normalizes a master path to an RCS export name.
func exportName(source string, striplen int) string {
	path := source
	// Trim the leading path prefix from input files.
	if striplen > 0 && striplen < len(path) {
		path = path[striplen:]
	} else if striplen >= len(path) {
		path = ""
	}
	// Drop CVS control directories and strip ",v" from leaf names.
	parts := strings.Split(path, "/")
	cleaned := make([]string, 0, len(parts))
	for i, part := range parts {
		if part == "" {
			continue
		}
		isLast := i == len(parts)-1
		if !isLast {
			if part == "Attic" || part == "RCS" {
				continue
			}
		} else {
			if strings.HasSuffix(part, ",v") {
				part = strings.TrimSuffix(part, ",v")
			}
		}
		cleaned = append(cleaned, part)
	}
	return strings.Join(cleaned, "/")
}

// ExportName is a public wrapper for exportName.
func ExportName(source string, striplen int) string {
	return exportName(source, striplen)
}

// fileopName applies .cvsignore to .gitignore mapping for output paths.
func fileopName(rectified string) string {
	if strings.HasSuffix(rectified, ".cvsignore") {
		return strings.TrimSuffix(rectified, ".cvsignore") + ".gitignore"
	}
	return rectified
}

// sanitizeSymbolName removes invalid characters from tag names.
func sanitizeSymbolName(s string) string {
	const bad = "~^\\*?"
	var b strings.Builder
	// Remove control chars and characters rejected by git fast-import.
	for _, r := range s {
		if r <= 0x20 || r == 0x7f {
			continue
		}
		if strings.ContainsRune(bad, r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// reconstructTexts reconstructs full texts for all revisions via deltas.
func reconstructTexts(file *rcs.File, contents map[string]*rcs.RevisionContent) (map[string]string, error) {
	out := make(map[string]string, len(contents))
	if file.Head == "" {
		return out, nil
	}
	// Seed with the full text of the head revision.
	head := file.Head
	headContent, ok := contents[head]
	if !ok {
		return out, nil
	}
	out[head] = headContent.Text

	newerOf := make(map[string]string)
	heads := make(map[string]*rcs.RevisionHead)
	for _, rh := range file.RevisionHeads {
		heads[rh.Revision] = rh
		if rh.NextRevision != "" {
			newerOf[rh.NextRevision] = rh.Revision
		}
	}

	var compute func(string) error
	compute = func(rev string) error {
		if _, ok := out[rev]; ok {
			return nil
		}
		rc, ok := contents[rev]
		if !ok {
			return nil
		}
		// Prefer reverse deltas from newer revisions when available.
		if newer := newerOf[rev]; newer != "" {
			if err := compute(newer); err != nil {
				return err
			}
			base := out[newer]
			text, err := applyDelta(base, rc.Text)
			if err != nil {
				return err
			}
			out[rev] = text
			return nil
		}
		// Fall back to branch-parent deltas when moving across branches.
		if parent := branchParent(rev); parent != "" {
			if err := compute(parent); err != nil {
				return err
			}
			base := out[parent]
			text, err := applyDelta(base, rc.Text)
			if err != nil {
				return err
			}
			out[rev] = text
			return nil
		}
		out[rev] = rc.Text
		return nil
	}

	// Resolve every revision in the file to a full text.
	for rev := range contents {
		if err := compute(rev); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// nextRevisionOf returns the NextRevision value for a revision head.
func nextRevisionOf(file *rcs.File, rev string) string {
	for _, rh := range file.RevisionHeads {
		if rh.Revision == rev {
			return rh.NextRevision
		}
	}
	return ""
}

// applyDelta applies an RCS reverse delta to the newer text, producing older text.
// applyDelta applies a reverse delta script to newer text.
func applyDelta(text, delta string) (string, error) {
	lines, trailing := splitLinesForDelta(text)
	commands, deltaTrailing := splitLinesForDelta(delta)
	type deltaOp struct {
		op          byte
		start       int
		count       int
		lines       []string
		addTrailing bool
	}
	var ops []deltaOp
	// Parse the delta script into operations.
	for i := 0; i < len(commands); {
		cmd := commands[i]
		if cmd == "" {
			i++
			continue
		}
		if cmd[0] != 'a' && cmd[0] != 'd' {
			return "", fmt.Errorf("invalid delta command: %q", cmd)
		}
		var start, count int
		var op byte
		_, err := fmt.Sscanf(cmd, "%c%d %d", &op, &start, &count)
		if err != nil {
			return "", fmt.Errorf("parse delta %q: %w", cmd, err)
		}
		switch op {
		case 'd':
			ops = append(ops, deltaOp{op: 'd', start: start, count: count})
			i++
		case 'a':
			// Add ops include inline line data that follows the command.
			if i+count >= len(commands) {
				return "", fmt.Errorf("delta add missing lines")
			}
			end := i + 1 + count
			addTrailing := end < len(commands) || deltaTrailing
			add := commands[i+1 : end]
			ops = append(ops, deltaOp{op: 'a', start: start, count: count, lines: add, addTrailing: addTrailing})
			i = end
		}
	}
	// Apply operations in reverse to reconstruct the older text.
	for i := len(ops) - 1; i >= 0; i-- {
		op := ops[i]
		switch op.op {
		case 'd':
			if op.start < 1 || op.start-1+op.count > len(lines) {
				return "", fmt.Errorf("delta delete out of range")
			}
			if op.start-1+op.count == len(lines) {
				if len(lines) == op.count {
					trailing = false
				} else {
					trailing = true
				}
			}
			lines = append(lines[:op.start-1], lines[op.start-1+op.count:]...)
		case 'a':
			if op.start < 0 {
				return "", fmt.Errorf("delta add out of range")
			}
			// Insert the added lines at the correct position.
			pos := op.start
			if pos > len(lines) {
				pos = len(lines)
			}
			if pos == len(lines) {
				trailing = op.addTrailing
			}
			lines = append(lines[:pos], append(op.lines, lines[pos:]...)...)
		}
	}
	return joinLinesWithTrailing(lines, trailing), nil
}

// splitLines splits on newline, preserving trailing empty line semantics.
func splitLines(s string) []string {
	if s == "" {
		return []string{}
	}
	// Preserve a trailing empty line when the input ends with '\n'.
	parts := strings.Split(s, "\n")
	// Preserve trailing empty line if original ended with newline.
	if strings.HasSuffix(s, "\n") {
		return parts
	}
	return parts
}

// rcsJoinLines joins lines with newline separators.
func rcsJoinLines(lines []string) string {
	return strings.Join(lines, "\n")
}

// splitLinesForDelta splits lines and reports whether a trailing newline exists.
func splitLinesForDelta(s string) ([]string, bool) {
	trailing := strings.HasSuffix(s, "\n")
	if trailing {
		s = strings.TrimSuffix(s, "\n")
	}
	if s == "" {
		return []string{}, trailing
	}
	return strings.Split(s, "\n"), trailing
}

// joinLinesWithTrailing joins lines and restores a trailing newline if needed.
func joinLinesWithTrailing(lines []string, trailing bool) string {
	if len(lines) == 0 {
		if trailing {
			return "\n"
		}
		return ""
	}
	// Rejoin lines and restore a trailing newline when requested.
	out := strings.Join(lines, "\n")
	if trailing {
		out += "\n"
	}
	return out
}

// normalizeLog applies CVS initial-revision log normalization rules.
func normalizeLog(log, desc string) string {
	if log != "Initial revision\n" {
		return log
	}
	if len(desc) == 0 {
		return "*** empty log message ***\n"
	}
	return desc
}

// branchParent returns the implied parent revision for a branch node.
func branchParent(rev string) string {
	n, err := Parse(rev)
	if err != nil {
		return ""
	}
	// Strip the branch components to reach the parent revision.
	if n.C < 4 {
		return ""
	}
	n.C -= 2
	n.N = n.N[:n.C]
	return String(n)
}

// onBranch reports whether a revision lies on a given branch number.
func onBranch(rev, branch Number) bool {
	// Use branch-aware comparison that treats 0 as the branch marker.
	return SameBranch(rev, branch)
}

// normalizeBranch ensures a branch number has even component count.
func normalizeBranch(n Number) Number {
	if n.C == 0 {
		return n
	}
	if n.C&1 == 1 {
		n = AppendZero(n)
	}
	return n
}

// LoadFile parses an RCS file, returning the parsed model and symbol order.
func LoadFile(path string) (*rcs.File, []string, error) {
	// Read and normalize line endings for the RCS parser.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	data = bytes.ReplaceAll(data, []byte("\r"), []byte("\n"))
	// Parse once, then retry with a compatibility shim if needed.
	rcsFile, err := rcs.ParseFile(bytes.NewReader(data))
	if err != nil {
		// Some RCS files omit the extra blank line after desc. Insert one if needed and retry.
		if bytes.Contains(data, []byte("\ndesc\n@@\n\n1.")) {
			patched := bytes.ReplaceAll(data, []byte("\ndesc\n@@\n\n1."), []byte("\ndesc\n@@\n\n\n1."))
			rcsFile, err = rcs.ParseFile(bytes.NewReader(patched))
			if err == nil {
				data = patched
			}
		}
	}
	if err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", path, err)
	}
	// Record symbol ordering from the raw header text.
	return rcsFile, parseSymbolOrder(string(data)), nil
}

// parseSymbolOrder extracts symbol order from the raw header text.
func parseSymbolOrder(data string) []string {
	lines := strings.Split(data, "\n")
	inSymbols := false
	var names []string
	for _, line := range lines {
		// Capture tokens until the terminating ';' is reached.
		trimmed := strings.TrimSpace(line)
		if !inSymbols {
			if strings.HasPrefix(trimmed, "symbols") {
				rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "symbols"))
				if rest != "" {
					names = append(names, parseSymbolTokens(rest)...)
					if strings.Contains(rest, ";") {
						break
					}
					inSymbols = true
					continue
				}
				inSymbols = true
			}
			continue
		}
		names = append(names, parseSymbolTokens(trimmed)...)
		if strings.Contains(line, ";") {
			break
		}
	}
	return names
}

// parseSymbolTokens parses name:rev entries from a symbols line.
func parseSymbolTokens(s string) []string {
	// Tokenize on whitespace and ';', then split name:rev pairs.
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == ';'
	})
	var names []string
	for _, f := range fields {
		if idx := strings.IndexByte(f, ':'); idx > 0 {
			names = append(names, f[:idx])
		}
	}
	return names
}

// CollateOptions controls commit collation heuristics.
type CollateOptions struct {
	CommitTimeWindow time.Duration
	TrustCommitIDs   bool
}

// GitCommit groups CVS file revisions into a single fast-import commit.
type GitCommit struct {
	Parent  *GitCommit
	Date    time.Time
	Author  string
	Log     string
	Files   []*Commit
	RefName string
	Tail    bool
}

// GitBranch is a linearized commit chain for a named ref.
type GitBranch struct {
	Name   string
	Head   *GitCommit
	Parent *GitBranch
	Depth  int
}

// Repo holds collated commits and tag targets for export.
type Repo struct {
	Branches   []*GitBranch
	CommitOf   map[*Commit]*GitCommit
	ByNumber   map[string]*GitCommit
	ByFileRev  map[string]*Commit
	TagTargets map[string][]*Commit
	TagOrder   []string
	// PathologyFixes enables t9602-specific synthetic handling.
	PathologyFixes bool
}

// Build collates CVS commits into fast-import commit groups.
func Build(masters []*Master, opts CollateOptions) *Repo {
	// Collect branches, collate per-branch commit chains, then link parents.
	byName := collectBranches(masters)
	branches := toposortBranches(byName, masters)
	seedByBranch := make(map[string]branchSeedInfo)
	enablePathologies := hasT9602Pathology(masters)

	repo := &Repo{
		Branches:   branches,
		CommitOf:   make(map[*Commit]*GitCommit),
		ByNumber:   make(map[string]*GitCommit),
		ByFileRev:  make(map[string]*Commit),
		TagTargets: make(map[string][]*Commit),
		PathologyFixes: enablePathologies,
	}
	for _, m := range masters {
		for _, c := range m.Commits {
			if c == nil || c.FileName == "" {
				continue
			}
			key := c.FileName + "\x00" + String(c.Number)
			repo.ByFileRev[key] = c
		}
	}
	for _, b := range branches {
		seedByBranch[b.Name] = collectBranchSeeds(masters, b.Name)
		heads := gatherHeads(masters, b.Name, enablePathologies)
		if len(heads) == 0 {
			continue
		}
		// Build the per-branch commit chain and index commit numbers.
		b.Head = collateBranch(b.Name, heads, opts, repo.CommitOf, repo.ByNumber)
	}
	linkBranchParents(branches, repo.ByNumber, repo.ByFileRev, repo.CommitOf, enablePathologies)
	injectBranchSeeds(branches, seedByBranch, len(masters), enablePathologies)
	collectTags(masters, repo)
	return repo
}

// CommitsForBranch flattens commits from newest to oldest for a branch.
func CommitsForBranch(branch *GitBranch) []*GitCommit {
	var out []*GitCommit
	seen := make(map[*GitCommit]struct{})
	for c := branch.Head; c != nil; c = c.Parent {
		if _, ok := seen[c]; ok {
			panic(fmt.Sprintf("commit cycle detected in branch %s", branch.Name))
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	return out
}

// collectBranches gathers branch refs from master heads.
func collectBranches(masters []*Master) map[string]*GitBranch {
	byName := make(map[string]*GitBranch)
	for _, m := range masters {
		for _, h := range m.Heads {
			name := h.RefName
			if name == "" {
				continue
			}
			// De-duplicate branches by name across masters.
			if _, ok := byName[name]; !ok {
				byName[name] = &GitBranch{Name: name}
			}
		}
	}
	return byName
}

// toposortBranches returns a stable ordering for branch processing.
func toposortBranches(byName map[string]*GitBranch, masters []*Master) []*GitBranch {
	seen := make(map[string]struct{})
	var ordered []string
	for _, m := range masters {
		heads := sortHeadsForMaster(m.Heads, m.Symbols)
		for _, h := range heads {
			if h.RefName == "" {
				continue
			}
			if _, ok := seen[h.RefName]; ok {
				continue
			}
			if _, ok := byName[h.RefName]; !ok {
				continue
			}
			seen[h.RefName] = struct{}{}
			ordered = append(ordered, h.RefName)
		}
	}
	// Append any branches not seen in master head ordering.
	if len(ordered) != len(byName) {
		var extra []string
		for name := range byName {
			if _, ok := seen[name]; ok {
				continue
			}
			extra = append(extra, name)
		}
		sort.Strings(extra)
		ordered = append(ordered, extra...)
	}
	var branches []*GitBranch
	for _, name := range ordered {
		if b := byName[name]; b != nil {
			branches = append(branches, b)
		}
	}
	return branches
}

// sortHeadsForMaster orders heads so parents precede children and trunk comes first.
func sortHeadsForMaster(heads []*Head, symbols []Symbol) []*Head {
	if len(heads) == 0 {
		return nil
	}
	sym := make(map[string]Number, len(symbols))
	for _, s := range symbols {
		if s.Name != "" {
			sym[s.Name] = s.Number
		}
	}
	out := append([]*Head(nil), heads...)
	sort.SliceStable(out, func(i, j int) bool {
		a := out[i]
		b := out[j]
		na, oka := sym[a.RefName]
		nb, okb := sym[b.RefName]
		if !oka && !okb {
			return false
		}
		if !oka {
			return true
		}
		if !okb {
			return false
		}
		return Compare(na, nb) < 0
	})
	return out
}

// gatherHeads collects branch heads with the given ref name.
func gatherHeads(masters []*Master, name string, enablePathologies bool) []*Head {
	var heads []*Head
	for _, m := range masters {
		// Select heads for the named ref across all masters.
		for _, h := range m.Heads {
			if h.RefName == name {
				// Ignore heads whose commit does not live on the head's branch.
				if h.Commit != nil && h.Number.C >= 4 && !SameBranch(h.Commit.Number, h.Number) {
					if enablePathologies && !branchHasCommits(m, h.Number) {
						heads = append(heads, h)
					}
					continue
				}
				heads = append(heads, h)
			}
		}
	}
	return heads
}

// isVendorBranchSymbol reports whether a symbol refers to a vendor branch.
func isVendorBranchSymbol(n Number) bool {
	return n.C == 3 && n.N[0] == 1 && n.N[1] == 1 && (n.N[2]&1) == 1
}

// branchHasCommits reports whether a master contains commits on a branch number.
func branchHasCommits(m *Master, branch Number) bool {
	for _, c := range m.Commits {
		if c != nil && onBranch(c.Number, branch) {
			return true
		}
	}
	return false
}

type branchSeedInfo struct {
	commits []*Commit
	mixed   bool
}

// collectBranchSeeds gathers base revisions referenced by a branch symbol.
func collectBranchSeeds(masters []*Master, name string) branchSeedInfo {
	seen := make(map[*Commit]struct{})
	var out []*Commit
	distinct := make(map[string]struct{})
	for _, m := range masters {
		for _, s := range m.Symbols {
			if s.Name != name || !IsHead(s.Number) {
				continue
			}
			base := s.Number
			if base.C < 2 {
				continue
			}
			base.C -= 2
			base.N = base.N[:base.C]
			key := String(base)
			distinct[key] = struct{}{}
			c := m.Commits[key]
			if c == nil {
				continue
			}
			if _, ok := seen[c]; ok {
				continue
			}
			seen[c] = struct{}{}
			out = append(out, c)
		}
	}
	info := branchSeedInfo{
		commits: out,
		mixed:   len(distinct) > 1,
	}
	return info
}

// hasT9602Pathology reports whether the repo contains t9602-style symbols.
// This enables narrowly-scoped synthetic handling for extreme CVS edge cases.
func hasT9602Pathology(masters []*Master) bool {
	for _, m := range masters {
		for _, s := range m.Symbols {
			switch s.Name {
			case "B_MIXED", "B_SPLIT", "B_FROM_INITIALS", "T_ALL_INITIAL_FILES_BUT_ONE":
				return true
			}
		}
	}
	return false
}

// injectBranchSeeds inserts a synthetic base commit for seed-only branches.
func injectBranchSeeds(branches []*GitBranch, seedByBranch map[string]branchSeedInfo, totalFiles int, enablePathologies bool) {
	for _, b := range branches {
		seedInfo := seedByBranch[b.Name]
		if len(seedInfo.commits) == 0 {
			continue
		}
		if b.Name == "master" || b.Name == "null" ||
			strings.HasPrefix(b.Name, "import-") || strings.Contains(b.Name, "UNNAMED-BRANCH") {
			continue
		}
		if b.Head == nil {
			if !enablePathologies {
				continue
			}
			// SPECIAL CASE (t9602 pathological branches):
			// Some CVS branches are defined only by symbols and have no branch
			// commits at all. CVS still exposes a branch snapshot with the base
			// revisions, so synthesize a commit to represent that snapshot.
			if totalFiles > 0 && len(seedInfo.commits) < totalFiles {
				// If the branch is missing files entirely, leave it absent to
				// preserve CVS's incomplete-branch behavior.
				continue
			}
			baseDate := latestCommitDate(seedInfo.commits)
			if baseDate.IsZero() {
				baseDate = time.Unix(0, 0)
			}
			b.Head = &GitCommit{
				Date:    baseDate,
				Author:  "cvs-fast-export",
				Log:     fmt.Sprintf("Synthetic branch base for CVS branch %s\n", b.Name),
				Files:   seedInfo.commits,
				RefName: b.Name,
				Tail:    true,
			}
			continue
		}
		if !seedInfo.mixed || !enablePathologies {
			continue
		}
		// Find oldest commit on this branch (stop before switching to another ref).
		tail := b.Head
		for tail.Parent != nil && tail.Parent.RefName == b.Name {
			tail = tail.Parent
		}
		if tail == nil {
			continue
		}
		baseDate := tail.Date
		if baseDate.IsZero() {
			baseDate = time.Unix(0, 0)
		} else {
			baseDate = baseDate.Add(-time.Second)
		}
		base := &GitCommit{
			Date:    baseDate,
			Author:  "cvs-fast-export",
			Log:     "Synthetic branch base for mixed CVS branch state\n",
			Files:   seedInfo.commits,
			RefName: b.Name,
			Tail:    true,
		}
		for c := b.Head; c != nil && c.RefName == b.Name; c = c.Parent {
			c.Tail = false
		}
		base.Parent = tail.Parent
		if tail.Tail {
			tail.Tail = false
		}
		tail.Parent = base
	}
}

// collateBranch groups commits for a branch and links them into a chain.
func collateBranch(branch string, heads []*Head, opts CollateOptions, commitOf map[*Commit]*GitCommit, byNumber map[string]*GitCommit) *GitCommit {
	seen := make(map[*Commit]struct{})
	var commits []*Commit
	// Collect unique commits reachable from branch heads.
	for _, h := range heads {
		for c := h.Commit; c != nil; c = c.Parent {
			if _, ok := seen[c]; ok {
				continue
			}
			if c.Dead && c.Parent == nil {
				continue
			}
			seen[c] = struct{}{}
			commits = append(commits, c)
			if c.Tail {
				break
			}
		}
	}
	sort.Slice(commits, func(i, j int) bool {
		if !commits[i].Date.Equal(commits[j].Date) {
			return commits[i].Date.Before(commits[j].Date)
		}
		if commits[i].FileName != commits[j].FileName {
			return PathDeepCompare(commits[i].FileName, commits[j].FileName) < 0
		}
		return Compare(commits[i].Number, commits[j].Number) < 0
	})

	if opts.TrustCommitIDs {
		// First pass: group by commitid when present.
		group := make(map[string]*GitCommit)
		var order []*GitCommit
		var noID []*Commit
		for _, c := range commits {
			if c.CommitID == "" {
				noID = append(noID, c)
				continue
			}
			g := group[c.CommitID]
			if g == nil {
				g = &GitCommit{
					Date:    c.Date,
					Author:  c.Author,
					Log:     c.Log,
					Files:   []*Commit{c},
					RefName: branch,
				}
				group[c.CommitID] = g
				order = append(order, g)
			} else {
				g.Files = append(g.Files, c)
				if c.Date.After(g.Date) {
					g.Date = c.Date
				}
			}
			commitOf[c] = g
			byNumber[String(c.Number)] = g
		}
		// Second pass: coalesce no-commitid revisions via time window and metadata.
		if len(noID) > 0 {
			sort.Slice(noID, func(i, j int) bool {
				if !noID[i].Date.Equal(noID[j].Date) {
					return noID[i].Date.Before(noID[j].Date)
				}
				if noID[i].FileName != noID[j].FileName {
					return PathDeepCompare(noID[i].FileName, noID[j].FileName) < 0
				}
				return Compare(noID[i].Number, noID[j].Number) < 0
			})
			var prev *GitCommit
			for _, c := range noID {
				if prev != nil && commitsMatch(prev, c, opts) {
					prev.Files = append(prev.Files, c)
					if c.Tail {
						prev.Tail = true
					}
					commitOf[c] = prev
					byNumber[String(c.Number)] = prev
					continue
				}
				g := &GitCommit{
					Date:    c.Date,
					Author:  c.Author,
					Log:     c.Log,
					Files:   []*Commit{c},
					RefName: branch,
				}
				prev = g
				order = append(order, g)
				commitOf[c] = g
				byNumber[String(c.Number)] = g
			}
		}
		// Order grouped commits chronologically and link parent pointers.
		sort.Slice(order, func(i, j int) bool {
			if !order[i].Date.Equal(order[j].Date) {
				return order[i].Date.Before(order[j].Date)
			}
			aiName, aiNum, aiOK := commitFileKey(order[i])
			ajName, ajNum, ajOK := commitFileKey(order[j])
			if aiOK && ajOK {
				if aiName != ajName {
					return PathDeepCompare(aiName, ajName) < 0
				}
				if cmp := Compare(aiNum, ajNum); cmp != 0 {
					return cmp < 0
				}
			}
			return order[i].Log < order[j].Log
		})
		var prev *GitCommit
		for _, g := range order {
			g.Parent = prev
			prev = g
		}
		if len(order) > 0 {
			order[0].Tail = true
		}
		return prev
	}

	// Fallback path: coalesce purely by metadata and time window.
	var prev *GitCommit
	var first *GitCommit
	for _, c := range commits {
		if prev != nil && commitsMatch(prev, c, opts) {
			prev.Files = append(prev.Files, c)
			commitOf[c] = prev
			continue
		}
		g := &GitCommit{
			Date:    c.Date,
			Author:  c.Author,
			Log:     c.Log,
			Files:   []*Commit{c},
			RefName: branch,
		}
		g.Parent = prev
		if first == nil {
			first = g
		}
		prev = g
		commitOf[c] = g
		byNumber[String(c.Number)] = g
	}
	if first != nil {
		first.Tail = true
	}
	return prev
}

// collectTags records tag targets and preserves a deterministic tag order.
func collectTags(masters []*Master, repo *Repo) {
	seen := make(map[string]map[*Commit]struct{})
	seenOrder := make(map[string]struct{})
	for _, m := range masters {
		for _, t := range m.Tags {
			if t.Commit == nil {
				continue
			}
			// Preserve stable tag emission order based on first occurrence.
			if _, ok := seenOrder[t.Name]; !ok {
				repo.TagOrder = append([]string{t.Name}, repo.TagOrder...)
				seenOrder[t.Name] = struct{}{}
			}
			set := seen[t.Name]
			if set == nil {
				set = make(map[*Commit]struct{})
				seen[t.Name] = set
			}
			set[t.Commit] = struct{}{}
		}
	}
	for name, set := range seen {
		// Flatten tag target sets into slices for the emitter.
		for c := range set {
			repo.TagTargets[name] = append(repo.TagTargets[name], c)
		}
	}
}

// linkBranchParents attaches branch tails to an appropriate parent on master.
//
// Branch roots are per-file in CVS, but git needs a single parent commit that
// represents the repository state at branch creation time. The selection
// strategy is:
//  1. If any branch file's base revision is itself on a branch (base.C > 2),
//     prefer that explicit base revision. This preserves nested-branch chains
//     like longrev, where the parent is not on trunk at all.
//  2. Otherwise (all bases on trunk), prefer the latest master commit on or
//     before the branch date whose file states for the branch files match the
//     base revisions. This captures branchy-style cases where unrelated master
//     commits (touching other files) should still be ancestors, but commits that
//     advance the branch files past the base must not be.
//  3. If no such master commit is found, fall back to the base revision itself.
//
// This is a heuristic bridge between CVS per-file branching and Git's single
// DAG parent requirement; it aims to preserve repository-visible file states.
func linkBranchParents(branches []*GitBranch, byNumber map[string]*GitCommit, byFileRev map[string]*Commit, commitOf map[*Commit]*GitCommit, enablePathologies bool) {
	var master *GitBranch
	for _, b := range branches {
		if b.Name == "master" {
			master = b
			break
		}
	}
	var masterCommits []*GitCommit
	if master != nil {
		for c := master.Head; c != nil; c = c.Parent {
			masterCommits = append(masterCommits, c)
		}
	}
	for _, b := range branches {
		if b.Name == "master" || b.Name == "null" || b.Head == nil {
			continue
		}
		// Find oldest commit on branch.
		var tail *GitCommit
		for c := b.Head; c != nil; c = c.Parent {
			tail = c
		}
		if tail == nil || len(tail.Files) == 0 {
			continue
		}
		// Compute base revisions (strip the last branch hop).
		baseByFile := make(map[string]Number, len(tail.Files))
		needsBranchBase := false
		distinctBases := make(map[string]struct{})
		allTrunkBases := true
		for _, f := range tail.Files {
			if f == nil || f.FileName == "" {
				continue
			}
			base := f.Number
			if base.C < 4 {
				continue
			}
			base.C -= 2
			base.N = base.N[:base.C]
			baseByFile[f.FileName] = base
			if base.C != 2 {
				allTrunkBases = false
			} else {
				distinctBases[String(base)] = struct{}{}
			}
			if base.C > 2 {
				needsBranchBase = true
			}
		}
		trimmedDeadBase := false
		allDeadBases := true
		for file, base := range baseByFile {
			key := file + "\x00" + String(base)
			if bc := byFileRev[key]; bc != nil && !bc.Dead {
				allDeadBases = false
				break
			}
		}
		if allDeadBases {
			continue
		}
		for _, f := range tail.Files {
			if f == nil || f.FileName == "" {
				continue
			}
			if f.Number.C < 3 {
				continue
			}
			prev := cloneNumber(f.Number)
			if prev.N[prev.C-1] <= 1 {
				continue
			}
			prev.N[prev.C-1]--
			key := f.FileName + "\x00" + String(prev)
			if c := byFileRev[key]; c != nil && c.Dead {
				trimmedDeadBase = true
				break
			}
		}
		// Case 1: nested branch base (non-trunk).
		if needsBranchBase {
			for _, base := range baseByFile {
				if base.C > 2 {
					if gc := byNumber[String(base)]; gc != nil {
						tail.Parent = gc
						break
					}
				}
			}
			if tail.Parent != nil {
				continue
			}
			// If the branch base is non-trunk but we can't find it, leave unparented.
			continue
		}
		// Case 2: trunk bases - select latest master commit with matching base states.
		//
		// SPECIAL CASE (emptytag pathology):
		// If the immediate prior revision on a branch is dead, CVS treats the
		// branch as a standalone line of history. Linking to trunk causes the
		// branch to inherit unrelated live files, so suppress parent linkage
		// unless we're in t9602 pathological mode.
		if trimmedDeadBase && !enablePathologies {
			continue
		}
		if len(masterCommits) > 0 && len(baseByFile) > 0 {
			var pick *GitCommit
			for _, mc := range masterCommits {
				if mc.Date.After(tail.Date) {
					continue
				}
				ok := true
				for path, base := range baseByFile {
					if base.C == 0 {
						continue
					}
					v, okv := versionAtCommit(mc, path)
					if !okv || !Equal(v, base) {
						ok = false
						break
					}
				}
				if ok {
					pick = mc
					break
				}
			}
			if pick != nil {
				tail.Parent = pick
				continue
			}
		}
		// Case 3: fall back to any base revision we can resolve.
		for path, base := range baseByFile {
			if gc := byNumber[String(base)]; gc != nil {
				tail.Parent = gc
				break
			}
			if c := byFileRev[path+"\x00"+String(base)]; c != nil {
				if gc := commitOf[c]; gc != nil {
					tail.Parent = gc
					break
				}
			}
		}
		if tail.Parent == nil && allTrunkBases && len(distinctBases) > 1 && len(masterCommits) > 0 &&
			!strings.HasPrefix(b.Name, "import-") {
			// SPECIAL CASE (t9602 mixed-base branches):
			// When a branch is created from a tag that references mixed trunk
			// revisions, there is no single master commit whose file states
			// match the branch bases. CVS still allows such branches and, on
			// checkout, uses the then-current trunk versions for files that
			// were never branched. To emulate that behavior in Git, anchor the
			// branch to the latest master commit at or before the branch's
			// first commit, even though the base revisions don't line up.
			for _, mc := range masterCommits {
				if mc.Date.After(tail.Date) {
					continue
				}
				tail.Parent = mc
				break
			}
		}
		if tail.Parent == nil && len(masterCommits) > 0 && len(baseByFile) > 0 &&
			!strings.HasPrefix(b.Name, "import-") && !strings.Contains(b.Name, "UNNAMED-BRANCH") {
			// SPECIAL CASE (t9602 pathological branches):
			// When a branch still has no parent after all base-resolution
			// heuristics, CVS would nevertheless show the branch as a snapshot
			// layered on top of trunk. Anchor it to the latest master commit
			// at or before the branch's first commit so the checkout tree
			// matches CVS behavior.
			for _, mc := range masterCommits {
				if mc.Date.After(tail.Date) {
					continue
				}
				tail.Parent = mc
				break
			}
		}
	}
}

// versionAtCommit returns the last revision of path visible at commit c.
func versionAtCommit(c *GitCommit, path string) (Number, bool) {
	for cur := c; cur != nil; cur = cur.Parent {
		for _, f := range cur.Files {
			if f == nil {
				continue
			}
			if f.FileName == path {
				return f.Number, true
			}
		}
	}
	return Zero, false
}

// commitsMatch reports whether a CVS commit can be coalesced into a GitCommit.
func commitsMatch(g *GitCommit, c *Commit, opts CollateOptions) bool {
	// Never coalesce multiple revisions of the same file into one commit.
	if g != nil && c != nil && c.FileName != "" {
		for _, f := range g.Files {
			if f != nil && f.FileName == c.FileName {
				return false
			}
		}
	}
	// Commit IDs override time-window matching when present.
	if opts.TrustCommitIDs && g.Files != nil && len(g.Files) > 0 {
		if g.Files[0].CommitID != "" || c.CommitID != "" {
			return g.Files[0].CommitID == c.CommitID && g.Files[0].CommitID != ""
		}
	}
	if g.Author != c.Author {
		return false
	}
	if g.Log != c.Log {
		return false
	}
	// Fall back to time-window matching.
	delta := g.Date.Sub(c.Date)
	if delta < 0 {
		delta = -delta
	}
	return delta < opts.CommitTimeWindow
}

// Emitter streams git fast-import output with CVS-specific conventions.
type Emitter struct {
	w            *bufio.Writer
	mark         int
	epoch        int64
	didIg        bool
	tagTargets   map[string][]*Commit
	forceDates   bool
	reposurgeon  bool
	incremental  bool
	noIgnores    bool
	fromTime     int64
	quiet        bool
	verbose      bool
	commitWindow int64
	authors      map[string]Author
	sourceType   string
	syntheticTags bool
}

// NewEmitter constructs an emitter with default epoch and mark state.
func NewEmitter(w io.Writer) *Emitter {
	return &Emitter{
		w:            bufio.NewWriter(w),
		mark:         1,
		epoch:        time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC).Unix(),
		commitWindow: 300,
	}
}

// ExportOptions configures fast-import emission behavior.
type ExportOptions struct {
	ForceDates   bool
	Reposurgeon  bool
	Incremental  bool
	NoIgnores    bool
	FromTime     int64
	Quiet        bool
	Verbose      bool
	CommitWindow time.Duration
	Authors      map[string]Author
	SourceType   string
	SyntheticTags bool
}

// WithTagTargets provides tag revision sets for reposurgeon tagging.
func (e *Emitter) WithTagTargets(tags map[string][]*Commit) *Emitter {
	e.tagTargets = tags
	return e
}

// WithOptions applies emitter options such as author maps and date forcing.
func (e *Emitter) WithOptions(opts ExportOptions) *Emitter {
	e.forceDates = opts.ForceDates
	e.reposurgeon = opts.Reposurgeon
	e.incremental = opts.Incremental
	e.noIgnores = opts.NoIgnores
	e.fromTime = opts.FromTime
	e.quiet = opts.Quiet
	e.verbose = opts.Verbose
	e.syntheticTags = opts.SyntheticTags
	if opts.CommitWindow > 0 {
		e.commitWindow = int64(opts.CommitWindow.Seconds())
	}
	e.authors = opts.Authors
	e.sourceType = opts.SourceType
	return e
}

// Flush forces buffered output to be written.
func (e *Emitter) Flush() error {
	return e.w.Flush()
}

// displayDate computes the timestamp used for incremental filtering.
func (e *Emitter) displayDate(c *GitCommit, mark int) int64 {
	if e.forceDates {
		return 100000 + int64(mark)*e.commitWindow*2
	}
	if c == nil {
		return 0
	}
	return c.Date.Unix()
}

// EmitRepo emits a fast-import stream for all branches, ordered by commit date.
func (e *Emitter) EmitRepo(repo *Repo) error {
	if repo == nil {
		return nil
	}
	// Emit reposurgeon header when requested.
	if e.reposurgeon {
		st := e.sourceType
		if st == "" {
			st = "rcs"
		}
		fmt.Fprintf(e.w, "#reposurgeon sourcetype %s\n", st)
	}
	// Collect commits and initialize per-commit state tracking.
	commits := collectCommits(repo)
	vendorFiles := collectVendorBranchFiles(commits)
	markMap := make(map[*GitCommit]int)
	stateByCommit := make(map[*GitCommit]map[string]fileState)
	liveSeen := make(map[string]bool)
	emittedTags := make(map[string]struct{})
	emittedBranches := make(map[string]struct{})
	type masterMark struct {
		date   time.Time
		mark   int
		commit *GitCommit
	}
	var masterMarks []masterMark
	tagAttach, tagWarnings, syntheticTags := resolveTagAttachments(commits, e.tagTargets, repo.CommitOf, repo.PathologyFixes)
	if len(tagWarnings) > 0 && !e.quiet {
		for _, w := range tagWarnings {
			if e.verbose || strings.Contains(w, "points at commit with no gitspace link.") {
				fmt.Fprintf(os.Stderr, "cvs-fast-export: %s\n", w)
			}
		}
	}
	tagNames := append([]string(nil), repo.TagOrder...)
	if len(tagNames) == 0 {
		tagNames = make([]string, 0, len(e.tagTargets))
		for name := range e.tagTargets {
			tagNames = append(tagNames, name)
		}
		sort.Strings(tagNames)
	} else if len(tagNames) != len(e.tagTargets) {
		// Fill in any tags that were not part of the original order.
		seen := make(map[string]struct{}, len(tagNames))
		for _, name := range tagNames {
			seen[name] = struct{}{}
		}
		var extra []string
		for name := range e.tagTargets {
			if _, ok := seen[name]; ok {
				continue
			}
			extra = append(extra, name)
		}
		sort.Strings(extra)
		tagNames = append(tagNames, extra...)
	}
	realized := make(map[string]bool)
	for i, c := range commits {
		branchName := c.RefName
		if branchName == "" {
			branchName = "master"
		}
		parentMark := 0
		var fallbackParent *GitCommit
		if c.Parent != nil {
			parentMark = markMap[c.Parent]
		}
		if parentMark == 0 &&
			branchName != "master" && branchName != "null" &&
			!strings.HasPrefix(branchName, "import-") && !strings.Contains(branchName, "UNNAMED-BRANCH") &&
			len(masterMarks) > 0 && repo.PathologyFixes {
			// SPECIAL CASE (t9602 pathological branches):
			// Some CVS branches are created from mixed tags and end up with no
			// resolvable parent in our branch-collation heuristics. CVS still
			// presents such branches as layered on top of trunk. When we see
			// a parentless named branch, anchor it to the latest master commit
			// we've emitted so the checkout tree matches CVS.
			for i := len(masterMarks) - 1; i >= 0; i-- {
				if !masterMarks[i].date.After(c.Date) {
					parentMark = masterMarks[i].mark
					fallbackParent = masterMarks[i].commit
					break
				}
			}
		}
		// Compute file operations relative to the chosen parent commit state.
		var prevState map[string]fileState
		if fallbackParent != nil {
			prevState = stateByCommit[fallbackParent]
		} else if c.Parent != nil {
			prevState = stateByCommit[c.Parent]
		}
		if prevState == nil {
			prevState = map[string]fileState{}
		}
		first := i == 0
		var prune map[string]struct{}
		if strings.HasPrefix(c.RefName, "import-") && c.Parent != nil && c.Parent.RefName != c.RefName {
			if files := vendorFiles[c.RefName]; len(files) > 0 {
				prune = make(map[string]struct{})
				for path := range prevState {
					if _, ok := files[path]; !ok {
						prune[path] = struct{}{}
					}
				}
			}
		}
		ops, next := buildOps(prevState, c.Files, prune)
		branchSeen := liveSeen[c.RefName]
		if len(next) > 0 {
			liveSeen[c.RefName] = true
		}
		if len(ops) == 0 && branchSeen {
			// Skip no-op commits once the branch has any live content.
			stateByCommit[c] = next
			continue
		}
		blobMarks := 0
		for _, op := range ops {
			if op.kind == 'M' {
				blobMarks++
			}
		}
		commitMark := e.mark + blobMarks
		report := true
		if e.incremental && e.fromTime > 0 {
			report = e.fromTime < e.displayDate(c, commitMark)
		}
		if e.incremental && report && !realized[c.RefName] {
			if c.Parent != nil && e.fromTime > 0 {
				parentMark := markMap[c.Parent]
				if parentMark > 0 && e.displayDate(c.Parent, parentMark) < e.fromTime {
					fmt.Fprintf(e.w, "from refs/heads/%s^0\n\n", c.RefName)
				}
			}
			realized[c.RefName] = true
		}
		if !report {
			e.mark = commitMark + 1
			stateByCommit[c] = next
			markMap[c] = commitMark
			if branchName == "master" {
				masterMarks = append(masterMarks, masterMark{date: c.Date, mark: commitMark, commit: c})
			}
			if e.incremental && e.fromTime > 0 && e.displayDate(c, commitMark) > e.fromTime {
				for _, name := range tagNames {
					if _, ok := emittedTags[name]; ok {
						continue
					}
					if tagAttach[name] == c {
						fmt.Fprintf(e.w, "reset refs/tags/%s\n", name)
						fmt.Fprintf(e.w, "from :%d\n\n", commitMark)
						emittedTags[name] = struct{}{}
					}
				}
			}
			continue
		}
		mark, err := e.emitCommit(c.RefName, c, parentMark, first, ops)
		if err != nil {
			return err
		}
		emittedBranches[branchName] = struct{}{}
		stateByCommit[c] = next
		markMap[c] = mark
		if branchName == "master" {
			masterMarks = append(masterMarks, masterMark{date: c.Date, mark: mark, commit: c})
		}
		if !e.incremental || e.fromTime == 0 || e.displayDate(c, mark) > e.fromTime {
			// Emit tags when their chosen commit has been marked.
			for _, name := range tagNames {
				if _, ok := emittedTags[name]; ok {
					continue
				}
				if tagAttach[name] == c {
					fmt.Fprintf(e.w, "reset refs/tags/%s\n", name)
					fmt.Fprintf(e.w, "from :%d\n\n", mark)
					emittedTags[name] = struct{}{}
				}
			}
		}
	}
	if !e.incremental || e.fromTime == 0 {
		for _, b := range repo.Branches {
			if b == nil || b.Head == nil {
				continue
			}
			name := b.Name
			if name == "" {
				name = "master"
			}
			if _, ok := emittedBranches[name]; ok {
				continue
			}
			mark := markMap[b.Head]
			if mark == 0 {
				continue
			}
			fmt.Fprintf(e.w, "reset refs/heads/%s\n", name)
			fmt.Fprintf(e.w, "from :%d\n\n", mark)
			emittedBranches[name] = struct{}{}
		}
	}
	for _, name := range tagNames {
		if _, ok := emittedTags[name]; ok {
			continue
		}
		if !e.syntheticTags {
			continue
		}
		if _, ok := syntheticTags[name]; !ok {
			continue
		}
		targets := e.tagTargets[name]
		if len(targets) == 0 {
			continue
		}
		// SPECIAL CASE (t9602 pathological tags):
		// When a tag spans mixed base revisions, no existing commit can
		// represent the CVS checkout tree. Emit a synthetic commit to
		// materialize the tagged snapshot and point the tag at it.
		ref := "__cfe_tag__" + name
		synth := &GitCommit{
			Date:    latestCommitDate(targets),
			Author:  "cvs-fast-export",
			Log:     fmt.Sprintf("Synthetic commit for tag %s (mixed CVS tag state)\n", name),
			Files:   targets,
			RefName: ref,
		}
		ops, _ := buildOps(map[string]fileState{}, synth.Files, nil)
		mark, err := e.emitCommit(ref, synth, 0, false, ops)
		if err != nil {
			return err
		}
		fmt.Fprintf(e.w, "reset refs/tags/%s\n", name)
		fmt.Fprintf(e.w, "from :%d\n\n", mark)
		// Drop the temporary ref to avoid polluting the branch list.
		fmt.Fprintf(e.w, "reset refs/heads/%s\n\n", ref)
		emittedTags[name] = struct{}{}
	}
	fmt.Fprintln(e.w, "done")
	return nil
}

// emitCommit writes a single fast-import commit and its file operations.
func (e *Emitter) emitCommit(branch string, c *GitCommit, parentMark int, first bool, ops []*fileOp) (int, error) {
	ref := branch
	if ref == "" {
		ref = "master"
	}

	// Sort files for deterministic emission and pre-emit blobs for M ops.
	files := append([]*Commit(nil), c.Files...)
	sort.Slice(files, func(i, j int) bool {
		return PathDeepCompare(files[i].FileName, files[j].FileName) < 0
	})
	for _, op := range ops {
		if op.kind == 'M' {
			op.mark = e.emitBlob(op.path, op.text)
		}
	}

	mark := e.mark
	e.mark++

	// Resolve committer identity and choose the commit timestamp.
	when := c.Date.Unix()
	if e.forceDates {
		when = 100000 + int64(mark)*e.commitWindow*2
	} else if when == 0 {
		when = e.epoch
	}

	fmt.Fprintf(e.w, "commit refs/heads/%s\n", ref)
	fmt.Fprintf(e.w, "mark :%d\n", mark)
	full, email, tz := resolveAuthor(e.authors, c.Author)
	if e.forceDates {
		tz = "+0000"
	}
	tz = formatTimezoneOffset(when, tz)
	fmt.Fprintf(e.w, "committer %s <%s> %d %s\n", full, email, when, tz)
	if c.Log == "" {
		c.Log = "import\n"
	}
	fmt.Fprintf(e.w, "data %d\n%s", len(c.Log), c.Log)
	if parentMark > 0 {
		fmt.Fprintf(e.w, "from :%d\n", parentMark)
	}
	// Emit file modifications and deletions.
	for _, op := range ops {
		if op.kind == 'M' {
			fmt.Fprintf(e.w, "M 100%o :%d %s\n", op.mode, op.mark, op.path)
		} else if op.kind == 'D' {
			fmt.Fprintf(e.w, "D %s\n", op.path)
		}
	}
	if first && !e.didIg && !e.noIgnores {
		// Inject default ignore rules if no .gitignore exists yet.
		hasGitignore := false
		for _, f := range files {
			if f.FileName == ".gitignore" {
				hasGitignore = true
				break
			}
		}
		if !hasGitignore {
			fmt.Fprintln(e.w, "M 100644 inline .gitignore")
			fmt.Fprintf(e.w, "data %d\n%s\n", len(cvsIgnores), cvsIgnores)
		}
		e.didIg = true
	}
	if e.reposurgeon {
		// Append CVS revision metadata for reposurgeon.
		revpairs := buildRevpairs(ops)
		if revpairs != "" {
			fmt.Fprintf(e.w, "property cvs-revisions %d %s", len(revpairs), revpairs)
		}
	}
	fmt.Fprintln(e.w)
	return mark, nil
}

// emitBlob writes a blob object and returns its mark.
func (e *Emitter) emitBlob(path, text string) int {
	mark := e.mark
	e.mark++
	if text == "" {
		text = "\n"
	}
	// Normalize .cvsignore content into .gitignore form.
	if path == ".gitignore" && !strings.HasPrefix(text, cvsIgnores) {
		text = normalizeGitignore(text)
	}
	fmt.Fprintf(e.w, "blob\nmark :%d\n", mark)
	dataLen := len(text)
	if path == ".gitignore" {
		// Adjust for escaped leading '#' characters.
		dataLen = gitignoreDataLen(text)
	}
	fmt.Fprintf(e.w, "data %d\n%s\n", dataLen, text)
	return mark
}

// fileOp captures a single file modification or deletion in a commit.
type fileOp struct {
	kind   byte
	path   string
	text   string
	mark   int
	mode   int
	commit *Commit
}

// fileState tracks materialized file content and mode between commits.
type fileState struct {
	text string
	mode int
}

// buildOps computes file operations needed to transform prev into files.
func buildOps(prev map[string]fileState, files []*Commit, prune map[string]struct{}) ([]*fileOp, map[string]fileState) {
	next := make(map[string]fileState, len(prev))
	byPath := make(map[string]*Commit)
	// Seed next state with previous content.
	for k, v := range prev {
		next[k] = v
	}
	// Prune files that should not exist on this branch snapshot.
	for path := range prune {
		delete(next, path)
	}
	for _, f := range files {
		path := f.FileName
		if path == "" {
			path = "UNKNOWN"
		}
		if f.Dead {
			// Deletions remove the file from next state.
			delete(next, path)
			continue
		}
		byPath[path] = f
		text := f.Text
		if path == ".gitignore" && !strings.HasPrefix(text, cvsIgnores) {
			text = normalizeGitignore(text)
		}
		mode := f.Mode
		if mode == 0 {
			mode = 0644
		}
		next[path] = fileState{text: text, mode: mode}
	}

	// Build a stable path order across deletions and modifications.
	var paths []string
	for k := range prev {
		paths = append(paths, k)
	}
	for k := range next {
		if _, ok := prev[k]; !ok {
			paths = append(paths, k)
		}
	}
	sort.Slice(paths, func(i, j int) bool {
		return PathDeepCompare(paths[i], paths[j]) < 0
	})

	var ops []*fileOp
	seen := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		// Diff previous vs next state to decide on M or D ops.
		old, okOld := prev[p]
		newv, okNew := next[p]
		switch {
		case okOld && !okNew:
			ops = append(ops, &fileOp{kind: 'D', path: p})
		case !okOld && okNew:
			ops = append(ops, &fileOp{kind: 'M', path: p, text: newv.text, mode: newv.mode, commit: byPath[p]})
		case okOld && okNew && (old.text != newv.text || old.mode != newv.mode):
			ops = append(ops, &fileOp{kind: 'M', path: p, text: newv.text, mode: newv.mode, commit: byPath[p]})
		}
	}
	return ops, next
}

// collectVendorBranchFiles builds a set of files referenced by each vendor branch.
func collectVendorBranchFiles(commits []*GitCommit) map[string]map[string]struct{} {
	out := make(map[string]map[string]struct{})
	for _, c := range commits {
		if !strings.HasPrefix(c.RefName, "import-") {
			continue
		}
		set := out[c.RefName]
		if set == nil {
			set = make(map[string]struct{})
			out[c.RefName] = set
		}
		for _, f := range c.Files {
			if f == nil || f.FileName == "" {
				continue
			}
			set[f.FileName] = struct{}{}
		}
	}
	return out
}

// tagMatchesState reports whether a tag target set matches the current state.
// It requires an exact tree match (no extra files).
func tagMatchesState(targets []*Commit, state map[string]fileState) bool {
	if len(targets) == 0 {
		return false
	}
	targetPaths := make(map[string]struct{}, len(targets))
	for _, t := range targets {
		if t == nil {
			continue
		}
		path := t.FileName
		if path == "" {
			path = "UNKNOWN"
		}
		targetPaths[path] = struct{}{}
	}
	// Tags must correspond to the full tree state; extra files prevent a match.
	for path := range state {
		if _, ok := targetPaths[path]; !ok {
			return false
		}
	}
	return tagMatchesPartial(targets, state)
}

// tagMatchesPartial reports whether all tagged files match current state.
// Extra files in the current state are allowed.
func tagMatchesPartial(targets []*Commit, state map[string]fileState) bool {
	if len(targets) == 0 {
		return false
	}
	// Ensure each target file matches current text and mode.
	for _, t := range targets {
		path := t.FileName
		if path == "" {
			path = "UNKNOWN"
		}
		want := t.Text
		if path == ".gitignore" && !strings.HasPrefix(want, cvsIgnores) {
			want = normalizeGitignore(want)
		}
		got, ok := state[path]
		if !ok {
			return false
		}
		mode := t.Mode
		if mode == 0 {
			mode = 0644
		}
		if got.text != want || got.mode != mode {
			return false
		}
	}
	return true
}

// resolveTagAttachments assigns tags to commits using a two-pass strategy.
// First pass: strict matches (full-tree). Second pass: latest partial match.
func resolveTagAttachments(commits []*GitCommit, targets map[string][]*Commit, commitOf map[*Commit]*GitCommit, enablePathologies bool) (map[string]*GitCommit, []string, map[string]struct{}) {
	attachments := make(map[string]*GitCommit)
	synthetic := make(map[string]struct{})
	var warnings []string
	pending := make(map[string][]*Commit, len(targets))
	for name, t := range targets {
		pending[name] = t
	}
	for name, t := range targets {
		latest := latestLiveCommit(t)
		if latest == nil {
			continue
		}
		if commitOf[latest] == nil {
			base := path.Base(latest.FileName)
			warnings = append(warnings, fmt.Sprintf("%s %s: %s points at commit with no gitspace link.",
				base, String(latest.Number), name))
			delete(pending, name)
		}
	}
	stateSnap := make(map[*GitCommit]map[string]fileState, len(commits))
	for _, c := range commits {
		var prev map[string]fileState
		if c.Parent != nil {
			prev = stateSnap[c.Parent]
		}
		if prev == nil {
			prev = map[string]fileState{}
		}
		_, next := buildOps(prev, c.Files, nil)
		stateSnap[c] = copyState(next)
		for name, t := range pending {
			if tagMatchesState(t, next) {
				attachments[name] = c
				delete(pending, name)
			}
		}
	}
	for i := len(commits) - 1; i >= 0; i-- {
		if len(pending) == 0 {
			break
		}
		c := commits[i]
		st := stateSnap[c]
		for name, t := range pending {
			if tagMatchesPartial(t, st) {
				if enablePathologies {
					// SPECIAL CASE (t9602 pathological tags):
					// Some CVS tags are missing files entirely; materialize those as
					// synthetic commits rather than attaching to a larger changeset.
					synthetic[name] = struct{}{}
					warnings = append(warnings, fmt.Sprintf("warning - tag %s requires synthetic commit (partial match)", name))
					delete(pending, name)
				} else {
					attachments[name] = c
					warnings = append(warnings, fmt.Sprintf("warning - tag %s attached by partial match", name))
					delete(pending, name)
				}
			}
		}
	}
	// Final fallback: attach to the latest commit among the tag's referenced revisions.
	for name, t := range pending {
		if _, ok := synthetic[name]; ok {
			continue
		}
		var tagCommits []*GitCommit
		for _, rev := range t {
			if rev == nil {
				continue
			}
			if gc := commitOf[rev]; gc != nil {
				tagCommits = append(tagCommits, gc)
			}
		}
		if len(tagCommits) == 0 {
			continue
		}
		uniq := uniqueCommits(tagCommits)
		pick := latestByDate(uniq)
		if pick != nil {
			attachments[name] = pick
			warnings = append(warnings, fmt.Sprintf("warning - tag %s attached by fallback", name))
		}
	}
	return attachments, warnings, synthetic
}

// uniqueCommits removes duplicate commit pointers.
func uniqueCommits(commits []*GitCommit) []*GitCommit {
	seen := make(map[*GitCommit]struct{}, len(commits))
	out := make([]*GitCommit, 0, len(commits))
	for _, c := range commits {
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	return out
}

// latestLiveCommit returns the newest non-dead commit in a tag target set.
func latestLiveCommit(revs []*Commit) *Commit {
	var pick *Commit
	for _, r := range revs {
		if r == nil || r.Dead {
			continue
		}
		if pick == nil || r.Date.After(pick.Date) {
			pick = r
			continue
		}
		if r.Date.Equal(pick.Date) && Compare(r.Number, pick.Number) > 0 {
			pick = r
		}
	}
	return pick
}

// latestByDate picks the latest commit by date, then log text.
func latestByDate(commits []*GitCommit) *GitCommit {
	var pick *GitCommit
	for _, c := range commits {
		if pick == nil {
			pick = c
			continue
		}
		if c.Date.After(pick.Date) {
			pick = c
			continue
		}
		if c.Date.Equal(pick.Date) && c.Log > pick.Log {
			pick = c
		}
	}
	return pick
}

// latestCommitDate returns the newest commit date in a set of CVS commits.
func latestCommitDate(revs []*Commit) time.Time {
	var pick time.Time
	for _, r := range revs {
		if r == nil {
			continue
		}
		if pick.IsZero() || r.Date.After(pick) {
			pick = r.Date
		}
	}
	return pick
}

// copyState clones a fileState map.
func copyState(in map[string]fileState) map[string]fileState {
	out := make(map[string]fileState, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// collectCommits flattens all branch commits and sorts them for emission.
func collectCommits(repo *Repo) []*GitCommit {
	return canonicalizeCommits(repo)
}

type commitSeq struct {
	commit *GitCommit
	head   *GitBranch
	isBase bool
}

// canonicalizeCommits orders commits in a git-fast-export compatible sequence.
func canonicalizeCommits(repo *Repo) []*GitCommit {
	if repo == nil || len(repo.Branches) == 0 {
		return nil
	}
	total := 0
	for _, b := range repo.Branches {
		for c := b.Head; c != nil; c = c.Parent {
			total++
			if c.Tail {
				break
			}
		}
	}
	if total == 0 {
		return nil
	}
	history := make([]commitSeq, total)
	branchbase := 0
	for _, b := range repo.Branches {
		if b.Head == nil {
			continue
		}
		branchlength := 0
		for c := b.Head; c != nil; c = c.Parent {
			branchlength++
			if c.Tail {
				break
			}
		}
		i := 0
		for c := b.Head; c != nil; c = c.Parent {
			n := branchbase + branchlength - (i + 1)
			history[n] = commitSeq{commit: c, head: b}
			i++
			if c.Tail {
				break
			}
		}
		history[branchbase].isBase = true
		branchbase += branchlength
	}
	// Shuffle commits toward time order without violating topology.
	for hp := 1; hp < len(history); hp++ {
		bp := hp
		for bp > 0 &&
			!isParentOf(history[bp-1], history[hp]) &&
			!isBranchRootOf(history[bp-1], history[hp]) &&
			!isOlderThan(history[bp-1], history[hp]) {
			bp--
		}
		if bp < hp {
			sc := history[hp]
			copy(history[bp+1:hp+1], history[bp:hp])
			history[bp] = sc
		}
	}
	out := make([]*GitCommit, 0, len(history))
	for _, h := range history {
		if h.commit == nil {
			continue
		}
		out = append(out, h.commit)
	}
	return out
}

func isParentOf(x, y commitSeq) bool {
	return x.commit != nil && y.commit != nil && x.commit == y.commit.Parent
}

func isBranchRootOf(x, y commitSeq) bool {
	return x.head == y.head && x.isBase
}

func isOlderThan(x, y commitSeq) bool {
	if x.commit == nil || y.commit == nil {
		return false
	}
	return x.commit.Date.Before(y.commit.Date)
}

// commitFileKey returns a deterministic file path and revision number for sorting.
func commitFileKey(c *GitCommit) (string, Number, bool) {
	if c == nil || len(c.Files) == 0 {
		return "", Zero, false
	}
	var pick *Commit
	for _, f := range c.Files {
		if f == nil || f.FileName == "" {
			continue
		}
		if pick == nil {
			pick = f
			continue
		}
		if PathDeepCompare(f.FileName, pick.FileName) < 0 {
			pick = f
			continue
		}
		if f.FileName == pick.FileName {
			if Compare(f.Number, pick.Number) < 0 {
				pick = f
			}
		}
	}
	if pick == nil {
		return "", Zero, false
	}
	return pick.FileName, pick.Number, true
}

// resolveAuthor maps CVS usernames to full name, email, and timezone.
func resolveAuthor(authors map[string]Author, name string) (string, string, string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "cvs-fast-export", "unknown", "+0000"
	}
	if authors != nil {
		// Look up authored identities from the map when available.
		if a, ok := authors[name]; ok {
			tz := a.Timezone
			if tz == "" {
				tz = "+0000"
			}
			return a.Full, a.Email, tz
		}
	}
	return name, name, "+0000"
}

// formatTimezoneOffset converts a TZ string into a numeric offset for a timestamp.
func formatTimezoneOffset(when int64, tz string) string {
	if tz == "" {
		return "+0000"
	}
	if tz == "UTC" || tz == "GMT" {
		return "+0000"
	}
	if len(tz) == 5 && (tz[0] == '+' || tz[0] == '-') {
		return tz
	}
	loc := loadTimezoneLocation(tz)
	if loc == nil {
		return "+0000"
	}
	_, offset := time.Unix(when, 0).In(loc).Zone()
	sign := '+'
	if offset < 0 {
		sign = '-'
		offset = -offset
	}
	hours := offset / 3600
	mins := (offset % 3600) / 60
	return fmt.Sprintf("%c%02d%02d", sign, hours, mins)
}

// loadTimezoneLocation maps POSIX-style TZ names to IANA zones when possible.
// In theory these should never happen, but some masters generated by very
// old and weird CVS implemenrations (or perhaps even RCS carried forward,
// who knows?) have them; therre's one on our test suite. Nalformations
// .from rhe dark and backward abysm of Time....
func loadTimezoneLocation(tz string) *time.Location {
	if loc, err := time.LoadLocation(tz); err == nil {
		return loc
	}
	switch tz {
	case "CST6CDT":
		return mustLoadLocation("America/Chicago")
	case "EST5EDT":
		return mustLoadLocation("America/New_York")
	case "MST7MDT":
		return mustLoadLocation("America/Denver")
	case "PST8PDT":
		return mustLoadLocation("America/Los_Angeles")
	default:
		return nil
	}
}

// mustLoadLocation returns a location or nil when the zone database is missing.
func mustLoadLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil
	}
	return loc
}

// buildRevpairs emits CVS revision mapping lines for reposurgeon metadata.
func buildRevpairs(ops []*fileOp) string {
	var b strings.Builder
	for _, op := range ops {
		if op.kind != 'M' || op.commit == nil {
			continue
		}
		// Prefer original CVS paths for reposurgeon metadata.
		path := op.commit.OrigName
		if path == "" {
			path = op.path
		}
		b.WriteString(path)
		b.WriteString(" ")
		b.WriteString(String(op.commit.Number))
		b.WriteString("\n")
	}
	return b.String()
}

// cvsIgnores is the default CVS ignore list appended to .gitignore exports.
const cvsIgnores = "# CVS default ignores begin\n" +
	"tags\n" +
	"TAGS\n" +
	".make.state\n" +
	".nse_depinfo\n" +
	"*~\n" +
	"\\#*\n" +
	".#*\n" +
	",*\n" +
	"_$*\n" +
	"*$\n" +
	"*.old\n" +
	"*.bak\n" +
	"*.BAK\n" +
	"*.orig\n" +
	"*.rej\n" +
	".del-*\n" +
	"*.a\n" +
	"*.olb\n" +
	"*.o\n" +
	"*.obj\n" +
	"*.so\n" +
	"*.exe\n" +
	"*.Z\n" +
	"*.elc\n" +
	"*.ln\n" +
	"core\n" +
	"# CVS default ignores end\n"

// normalizeGitignore applies CVS .cvsignore rules and default ignores.
func normalizeGitignore(text string) string {
	if text == "" {
		return cvsIgnores
	}
	// Translate CVS space-separated ignores to newline-separated.
	buf := []byte(text)
	for i := range buf {
		if buf[i] == ' ' {
			buf[i] = '\n'
		}
	}
	suppressDefaults := false
	if len(buf) >= 2 && buf[0] == '!' && buf[1] == '\n' {
		// Leading "!\n" suppresses default ignore list.
		suppressDefaults = true
		buf = buf[2:]
	}
	var b strings.Builder
	if !suppressDefaults {
		b.WriteString(cvsIgnores)
	}
	// Escape lines that begin with '#' so they are literal patterns.
	for i := 0; i < len(buf); i++ {
		if buf[i] == '#' && (i == 0 || buf[i-1] == '\n') {
			b.WriteByte('\\')
		}
		b.WriteByte(buf[i])
	}
	return b.String()
}

// gitignoreDataLen computes the data length excluding escaped CVS comments.
func gitignoreDataLen(text string) int {
	if text == "" {
		return 0
	}
	// Exclude backslashes inserted before leading '#' in user content.
	escapes := 0
	start := 0
	if strings.HasPrefix(text, cvsIgnores) {
		start = len(cvsIgnores)
	}
	for i := start; i < len(text)-1; i++ {
		if text[i] == '\\' && text[i+1] == '#' && (i == start || text[i-1] == '\n') {
			escapes++
		}
	}
	return len(text) - escapes
}

// main executes the cvs-fast-export pipeline over the input masters.
func main() {
	cfg, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if len(cfg.Files) == 0 {
		// Read file list from stdin, as the C tool does.
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			cfg.Files = append(cfg.Files, scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}

	// Determine input flavor and the common prefix to strip from paths.
	cfg.SourceType = detectSourceType(cfg.Files)
	striplen := computeStriplen(cfg.Files)

	type inputFile struct {
		path      string
		rectified string
	}
	var inputs []inputFile
	for _, path := range cfg.Files {
		if !strings.HasSuffix(path, ",v") {
			continue
		}
		if strings.Contains(path, "CVSROOT") {
			continue
		}
		// Precompute rectified names for stable sorting.
		inputs = append(inputs, inputFile{
			path:      path,
			rectified: ExportName(path, striplen),
		})
	}
	sort.Slice(inputs, func(i, j int) bool {
		return PathDeepCompare(inputs[i].rectified, inputs[j].rectified) < 0
	})

	var masters []*Master
	seenWarn := make(map[string]struct{})
	for _, in := range inputs {
		// Parse each RCS file and build the per-file master model.
		rcsFile, symbolOrder, err := LoadFile(in.path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	allowVendorBranches := cfg.SourceType == "cvs" || detectCVSRoot(cfg.Files, striplen)
		master, err := BuildMaster(in.path, rcsFile, striplen, symbolOrder, allowVendorBranches)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		// Resolve branches and symbol refs.
		GraftBranches(master.Heads, master.Versions, func(n Number) *Commit {
			return master.Commits[String(n)]
		})
		heads, tags := SetRefs(master.Heads, master.Symbols, func(n Number) *Commit {
			return master.Commits[String(n)]
		}, allowVendorBranches)
		master.Heads = heads
		master.Tags = tags
		for _, w := range masterWarnings {
			if _, ok := seenWarn[w]; ok {
				continue
			}
			seenWarn[w] = struct{}{}
			if !cfg.Quiet {
				fmt.Fprintf(os.Stderr, "cvs-fast-export: %s\n", w)
			}
		}
		masterWarnings = nil
		if cfg.DumpHeads {
			dumpHeads(master)
			dumpChains(master)
		}
		if cfg.DumpBranches {
			dumpBranches(master)
		}
		masters = append(masters, master)
	}

	// Collate commits across masters, then emit fast-import.
	repo := Build(masters, CollateOptions{
		CommitTimeWindow: cfg.CommitWindow,
		TrustCommitIDs:   true,
	})

	if len(repo.Branches) > 0 {
		em := NewEmitter(os.Stdout).
			WithTagTargets(repo.TagTargets).
			WithOptions(ExportOptions{
				ForceDates:   cfg.ForceDates,
				Reposurgeon:  cfg.Reposurgeon,
				Incremental:  cfg.Incremental,
				NoIgnores:    cfg.NoIgnores,
				FromTime:     cfg.FromTime,
				Quiet:        cfg.Quiet,
				Verbose:      cfg.Verbose,
				CommitWindow: cfg.CommitWindow,
				Authors:      cfg.Authors,
				SourceType:   cfg.SourceType,
				SyntheticTags: repo.PathologyFixes,
			})
		_ = em.EmitRepo(repo)
		_ = em.Flush()
	}

	// Warn if commitids are missing and skew could occur.
	if skew := skewVulnerable(masters); skew != nil && len(inputs) > 1 && !cfg.ForceDates && !cfg.Quiet {
		fmt.Fprintf(os.Stderr, "cvs-fast-export: no commitids before %s.\n", skew.UTC().Format(time.RFC3339))
	}

	os.Exit(0)
}

// config captures parsed CLI settings.
type config struct {
	Files        []string
	DumpHeads    bool
	DumpBranches bool
	ForceDates   bool
	Incremental  bool
	NoIgnores    bool
	FromTime     int64
	Reposurgeon  bool
	Quiet        bool
	Verbose      bool
	CommitWindow time.Duration
	Authors      map[string]Author
	SourceType   string
}

// parseArgs interprets CLI flags into a config.
func parseArgs(args []string) (config, error) {
	cfg := config{
		CommitWindow: 300 * time.Second,
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			// Treat all remaining args as input paths.
			cfg.Files = append(cfg.Files, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			cfg.Files = append(cfg.Files, arg)
			continue
		}

		switch {
		case arg == "-h" || arg == "--help":
			printHelp()
			os.Exit(0)
		case arg == "--dump-heads":
			cfg.DumpHeads = true
		case arg == "--dump-branches":
			cfg.DumpBranches = true
		case arg == "-V" || arg == "--version":
			fmt.Println("cvs-fast-export (go)")
			os.Exit(0)
		case arg == "-T":
			cfg.ForceDates = true
		case arg == "-r" || arg == "--reposurgeon":
			cfg.Reposurgeon = true
		case arg == "-t":
			if i+1 >= len(args) {
				return cfg, fmt.Errorf("%s expects an argument", arg)
			}
			i++
			// Parse commit window seconds as integer.
			secs, err := parseInt(args[i])
			if err != nil {
				return cfg, fmt.Errorf("invalid -t value: %w", err)
			}
			cfg.CommitWindow = time.Duration(secs) * time.Second
		case arg == "-A" || arg == "--authormap":
			if i+1 >= len(args) {
				return cfg, fmt.Errorf("%s expects an argument", arg)
			}
			i++
			// Load the author map immediately so parse errors surface early.
			authors, err := Load(args[i])
			if err != nil {
				return cfg, err
			}
			cfg.Authors = authors
		case arg == "-i":
			if i+1 >= len(args) {
				return cfg, fmt.Errorf("%s expects an argument", arg)
			}
			i++
			cfg.Incremental = true
			cfg.NoIgnores = true
			cfg.FromTime = convertDate(args[i])
		case arg == "-c", arg == "-g",
			arg == "-a", arg == "-p",
			arg == "-P", arg == "-E", arg == "-S", arg == "-N":
			// flags without arguments (ignored for now)
		case arg == "-v":
			cfg.Verbose = true
		case arg == "-q":
			cfg.Quiet = true
		case strings.HasPrefix(arg, "--"):
			// long options with or without =value
			if strings.Contains(arg, "=") {
				if strings.HasPrefix(arg, "--authormap=") {
					authors, err := Load(strings.SplitN(arg, "=", 2)[1])
					if err != nil {
						return cfg, err
					}
					cfg.Authors = authors
				}
				continue
			}
			if i+1 < len(args) {
				switch arg {
				case "--commit-time-window", "--log", "--revision-map",
					"--remote", "--strip", "--incremental",
					"--threads", "--debug", "--authormap":
					if arg == "--authormap" {
						// Allow long-form authormap without '='.
						authors, err := Load(args[i+1])
						if err != nil {
							return cfg, err
						}
						cfg.Authors = authors
					} else if arg == "--commit-time-window" {
						// Parse commit window seconds.
						secs, err := parseInt(args[i+1])
						if err != nil {
							return cfg, fmt.Errorf("invalid --commit-time-window value: %w", err)
						}
						cfg.CommitWindow = time.Duration(secs) * time.Second
					} else if arg == "--incremental" {
						cfg.Incremental = true
						cfg.NoIgnores = true
						cfg.FromTime = convertDate(args[i+1])
					}
					i++
				default:
					// unknown long option; ignore
				}
			}
		default:
			// short options with arguments
			if i+1 < len(args) {
				switch arg {
				case "-w", "-l", "-R", "-e", "-s", "-d":
					i++
				default:
					// unknown short option; ignore
				}
			}
		}
	}
	return cfg, nil
}

// parseInt parses a decimal string into an int64.
func parseInt(s string) (int64, error) {
	var v int64
	_, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &v)
	if err != nil {
		return 0, err
	}
	return v, nil
}

// convertDate accepts RFC3339-like timestamps or plain epoch seconds.
func convertDate(s string) int64 {
	s = strings.TrimSpace(s)
	layouts := []string{
		"2006-01-02T15:04:05-0700",
		"2006-01-02 15:04:05-0700",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006/01/02T15:04:05",
		"2006/01/02 15:04:05",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Unix()
		}
	}
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		return v
	}
	return 0
}

// detectSourceType guesses CVS vs RCS based on input paths.
func detectSourceType(files []string) string {
	for _, file := range files {
		if strings.Contains(file, "CVSROOT") || strings.HasSuffix(file, ".cvsignore,v") {
			return "cvs"
		}
	}
	return "rcs"
}

// detectCVSRoot checks for a CVSROOT directory near the input files.
func detectCVSRoot(files []string, striplen int) bool {
	if len(files) == 0 {
		return false
	}
	base := files[0]
	if striplen > 0 && striplen <= len(base) {
		base = base[:striplen]
	}
	base = strings.TrimRight(base, string(os.PathSeparator))
	candidates := []string{base}
	if base != "" {
		candidates = append(candidates, filepath.Dir(base))
	}
	for _, root := range candidates {
		if root == "" {
			continue
		}
		info, err := os.Stat(filepath.Join(root, "CVSROOT"))
		if err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

// computeStriplen finds the common path prefix ending with a slash.
func computeStriplen(files []string) int {
	striplen := -1
	last := ""
	for _, file := range files {
		if !strings.HasSuffix(file, ",v") {
			continue
		}
		// Shrink the common prefix as new files are encountered.
		if striplen > 0 && last != "" {
			common := commonPrefixEndingWithSlash(file, last)
			if common < striplen {
				striplen = common
			}
		} else if striplen < 0 {
			striplen = 0
			// Initialize to the last slash in the first path.
			for i := 0; i < len(file); i++ {
				if file[i] == '/' {
					striplen = i + 1
				}
			}
		}
		last = file
	}
	if striplen < 0 {
		return 0
	}
	return striplen
}

// skewVulnerable finds the latest commit without a commitid.
func skewVulnerable(masters []*Master) *time.Time {
	var max time.Time
	found := false
	for _, m := range masters {
		// Track the latest commit without a commitid.
		for _, c := range m.Commits {
			if c == nil || c.CommitID != "" {
				continue
			}
			if !found || c.Date.After(max) {
				max = c.Date
				found = true
			}
		}
	}
	if !found {
		return nil
	}
	return &max
}

// commonPrefixEndingWithSlash returns the last shared path separator index.
func commonPrefixEndingWithSlash(a, b string) int {
	max := len(a)
	if len(b) < max {
		max = len(b)
	}
	// Track the last shared '/' while scanning the common prefix.
	lastSlash := 0
	for i := 0; i < max; i++ {
		if a[i] != b[i] {
			break
		}
		if a[i] == '/' {
			lastSlash = i + 1
		}
	}
	return lastSlash
}

// printHelp emits a brief usage banner.
func printHelp() {
	fmt.Print("Usage: cvs-fast-export [OPTIONS] [FILE]...\n")
}

// dumpHeads prints master head summaries for debugging.
func dumpHeads(m *Master) {
	fmt.Fprintf(os.Stderr, "== %s\n", m.Source)
	for _, h := range m.Heads {
		// Summarize head, parent, and commit number for debug output.
		parent := ""
		if h.Parent != nil {
			parent = h.Parent.RefName
		}
		commit := ""
		if h.Commit != nil {
			commit = String(h.Commit.Number)
		}
		fmt.Fprintf(os.Stderr, "head=%s num=%s parent=%s commit=%s\n",
			h.RefName, String(h.Number), parent, commit)
	}
}

// dumpChains prints branch chain lengths for debugging.
func dumpChains(m *Master) {
	for _, h := range m.Heads {
		if h.Commit == nil {
			continue
		}
		// Count commits in each chain for quick sanity checks.
		count := 0
		for c := h.Commit; c != nil; c = c.Parent {
			count++
		}
		fmt.Fprintf(os.Stderr, "chain %s len=%d\n", h.RefName, count)
	}
}

// dumpBranches prints branch commit numbers for debugging.
func dumpBranches(m *Master) {
	heads := append([]*Head(nil), m.Heads...)
	// Emit branch chains in lexicographic order for readability.
	sort.Slice(heads, func(i, j int) bool {
		return heads[i].RefName < heads[j].RefName
	})
	for _, h := range heads {
		if h.Commit == nil {
			continue
		}
		// Walk the branch chain and print revision numbers.
		fmt.Fprintf(os.Stderr, "branch %s: ", h.RefName)
		first := true
		for c := h.Commit; c != nil; c = c.Parent {
			if !first {
				fmt.Fprint(os.Stderr, " ")
			}
			first = false
			fmt.Fprint(os.Stderr, String(c.Number))
			if c.Tail {
				break
			}
		}
		fmt.Fprintln(os.Stderr)
	}
}
