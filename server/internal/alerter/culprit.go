package alerter

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/towertail/towertail/server/internal/clickhouse"
)

// culprit is the process that causes an alert. Its label follows the
// rules of the Mac app (ProcCulprit), so a notification reads the same.
type culprit struct {
	pid                  int32
	name, cmd, cwd, repo string
}

func newCulprit(p clickhouse.ProcRow) culprit {
	return culprit{pid: p.PID, name: p.Name, cmd: p.Cmd, cwd: p.Cwd, repo: p.Repo}
}

const (
	maxNameRunes = 24
	maxWhatRunes = 28
	maxWords     = 2
	maxWordRunes = 20
)

// valueFlags take the next token as a value.
var valueFlags = map[string][]string{
	"node":   {"-r", "--require", "--import", "--loader"},
	"python": {"-W", "-X"},
	"java":   {"-cp", "-classpath", "--class-path"},
}

// subcommandTools show the tool name before the subcommand.
var subcommandTools = []string{"go", "bun", "deno"}

// what names the script that an interpreter runs, for example
// "vitest run". It is empty when the command gives no clear answer.
func (c culprit) what() string {
	tool := interpreter(c.name)
	toks := tokens(c.cmd)
	if tool == "" || len(toks) < 2 {
		return ""
	}
	toks = toks[1:]
	var script string
	var rest []string
	named := false // script comes from -m or -jar
scan:
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		switch {
		case !strings.HasPrefix(t, "-"):
			script, rest = t, toks[i+1:]
			break scan
		case tool == "node" && (t == "-e" || t == "--eval"), tool == "python" && t == "-c":
			return ""
		case tool == "python" && t == "-m", tool == "java" && t == "-jar":
			if i+1 >= len(toks) {
				return ""
			}
			script, rest, named = toks[i+1], toks[i+2:], true
			break scan
		case slices.Contains(valueFlags[tool], t):
			i++
		}
	}
	var out []string
	if slices.Contains(subcommandTools, tool) && isPlainWord(script) {
		out = append(out, tool)
	}
	script = lastPart(script, "/\\")
	if tool == "java" && !named {
		script = lastPart(script, ".")
	}
	if script == "" {
		return ""
	}
	out = append(out, script)
	for _, t := range rest[:min(len(rest), maxWords)] {
		if !isPlainWord(t) {
			break
		}
		out = append(out, t)
	}
	return cut(strings.Join(out, " "), maxWhatRunes)
}

// folder is the repo name, with the task of a .wt/<task> worktree, or
// the name of the working folder when there is no repo.
func (c culprit) folder() string {
	path := c.repo
	if path == "" {
		path = c.cwd
	}
	parts := strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) == 0 {
		return ""
	}
	name := parts[len(parts)-1]
	for i := 0; i+2 < len(parts); i++ {
		if parts[i] == ".wt" {
			return fmt.Sprintf("%s (.wt/%s)", name, parts[i+1])
		}
	}
	return name
}

// label is "<name> · <folder> · <what>" without the empty parts. A what
// that starts with the name replaces the name.
func (c culprit) label() string {
	name, what := c.name, c.what()
	if what != "" && strings.HasPrefix(what, name+" ") {
		name, what = what, ""
	} else {
		name = cut(c.displayName(), maxNameRunes)
	}
	var parts []string
	for _, p := range []string{name, c.folder(), what} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " · ")
}

// displayName is the bundle for a macOS app process, else the name.
func (c culprit) displayName() string {
	if i := strings.Index(c.cmd, ".app/"); i >= 0 {
		if start := strings.LastIndex(c.cmd[:i], "/") + 1; start < i {
			return c.cmd[start:i] + ".app"
		}
	}
	if c.name == "" {
		return fmt.Sprintf("pid %d", c.pid)
	}
	return c.name
}

// interpreter gives the lower case tool name without ".exe" and version
// ("python3.12" is "python"), or "" when the name is not a known tool.
func interpreter(name string) string {
	n := strings.ToLower(name)
	n = strings.TrimSuffix(n, ".exe")
	n = strings.TrimRight(n, "0123456789.")
	switch n {
	case "node", "python", "ruby", "java", "bun", "deno", "go":
		return n
	}
	return ""
}

// tokens splits on white space. A pair of double quotes keeps one token.
func tokens(cmd string) []string {
	var out []string
	var cur strings.Builder
	quoted, open := false, false
	flush := func() {
		if open {
			out = append(out, cur.String())
			cur.Reset()
			open = false
		}
	}
	for _, r := range cmd {
		switch {
		case r == '"':
			quoted, open = !quoted, true
		case unicode.IsSpace(r) && !quoted:
			flush()
		default:
			cur.WriteRune(r)
			open = true
		}
	}
	flush()
	return out
}

// isPlainWord is a word that starts with a letter and has letters,
// digits, "-", "_" or ":", up to 20 characters.
func isPlainWord(s string) bool {
	rs := []rune(s)
	if len(rs) == 0 || len(rs) > maxWordRunes || !unicode.IsLetter(rs[0]) {
		return false
	}
	for _, r := range rs {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("-_:", r) {
			return false
		}
	}
	return true
}

// lastPart gives the text after the last of the separators.
func lastPart(s, seps string) string {
	if i := strings.LastIndexAny(s, seps); i >= 0 {
		return s[i+1:]
	}
	return s
}

// cut keeps n-1 characters and an ellipsis when s is longer than n.
func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
