// SPDX-License-Identifier: GPL-2.0+
package authormap

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

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
		line := scanner.Text()
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
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
		tz := strings.TrimSpace(emailPart[gt+1:])
		m[name] = Author{Full: full, Email: email, Timezone: tz}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return m, nil
}
