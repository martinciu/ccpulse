package config

import (
	"bufio"
	"bytes"
	"strings"
)

// scaffoldHeader is prepended to Scaffold() output. Keep in sync with
// the user-facing copy in docs / CLAUDE.md if either changes.
const scaffoldHeader = `# ccpulse config — managed by you, never overwritten.
# See "ccpulse config show" for the live values (defaults + your overrides).
`

// Scaffold returns the bytes written to ~/.config/ccpulse/config.toml
// on first run. Format: scaffoldHeader followed by the embedded
// default.toml with every key=value line prefixed by "# ". Section
// headers stay active so uncommenting any single key Just Works (TOML
// allows empty sections; the in-code defaults still apply because the
// loader layers default.toml before the user file).
//
// Visual framing: each commented key gets a "#" separator line above
// it when the previous emitted line was a doc-comment, plus a single
// blank line below. Consecutive blank lines are deduped.
func Scaffold() []byte {
	var out bytes.Buffer
	out.WriteString(scaffoldHeader)
	out.WriteByte('\n') // blank line between header and first section

	sc := bufio.NewScanner(bytes.NewReader(defaultTOML))
	// default.toml has long doc-comment lines; give the scanner room.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var prevDocComment bool // last emitted non-blank line started with '#'
	var prevBlank bool      // last emitted line was blank — dedupes blanks

	emit := func(line string) {
		if line == "" {
			if prevBlank {
				return
			}
			out.WriteByte('\n')
			prevBlank = true
			prevDocComment = false
			return
		}
		out.WriteString(line)
		out.WriteByte('\n')
		prevBlank = false
		prevDocComment = strings.HasPrefix(strings.TrimLeft(line, " \t"), "#")
	}

	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)

		switch {
		case trimmed == "":
			emit("")
		case isSectionHeader(trimmed):
			emit(line)
		case strings.HasPrefix(trimmed, "#"):
			emit(line)
		default:
			// key = value — comment it out with framing.
			if prevDocComment {
				emit("#")
			}
			emit("# " + line)
			emit("")
		}
	}
	return out.Bytes()
}

// isSectionHeader reports whether s is a TOML [section] or
// [[array-of-tables]] header. s must already be whitespace-trimmed.
func isSectionHeader(s string) bool {
	if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
		return false
	}
	// Strip one bracket from each side; for [[arr]] this leaves "[arr]".
	inner := s[1 : len(s)-1]
	return inner != ""
}
