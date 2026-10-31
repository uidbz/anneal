package permission

import (
	"path"
	"regexp"
	"strings"
)

// ReadOnlyBash reports whether every segment of a shell command only reads.
// The classifier is deliberately conservative: anything it does not fully
// understand asks, because "read-only" is not the same as "harmless" (the
// caller still applies the outside-project and secret-file checks).
//
// Redirections, substitutions, unknown flags and commands that can write
// through an option disqualify a segment. The goal is to stop prompting for
// the bread-and-butter inspection commands (ls/cat/grep/sed -n/git status/go
// env) without weakening the jail around anything that mutates state.
func ReadOnlyBash(command string) bool {
	scan := ScanCommand(command)
	if len(scan.Commands) == 0 {
		return false
	}
	for _, c := range scan.Commands {
		if !readOnlySegment(c.Resource) {
			return false
		}
	}
	return true
}

// IsVerifyCommand names build/test/lint commands. They execute project code,
// so they stay "ask"; the label is for documentation and future presets.
func IsVerifyCommand(command string) bool {
	scan := ScanCommand(command)
	if len(scan.Commands) == 0 {
		return false
	}
	for _, c := range scan.Commands {
		if !verifySegment(c.Resource) {
			return false
		}
	}
	return true
}

func readOnlySegment(segment string) bool {
	// Pipes are split before classification; backticks and substitutions are
	// impossible to analyze. An escaped pipe inside a quoted grep pattern
	// (`"a\|b"`) is not a pipe and must not disqualify the segment.
	if strings.Contains(segment, "`") || strings.Contains(segment, "$(") {
		return false
	}
	// Redirections to a real file are writes; `2>&1`, `2>/dev/null` and
	// `< /dev/null` are noise that inspection commands use constantly.
	if hasWritingRedirection(segment) {
		return false
	}
	tokens := shellWords(segment)
	tokens = stripAssignments(tokens)
	if len(tokens) == 0 {
		return false
	}
	name := path.Base(strings.ToLower(tokens[0]))
	args := tokens[1:]

	switch name {
	case "ls", "dir", "pwd", "stat", "file", "du", "df", "head", "tail",
		"cat", "wc", "cut", "tr", "sort", "uniq", "diff", "cmp", "comm", "nl",
		"basename", "dirname", "realpath", "readlink", "which", "whereis",
		"type", "id", "whoami", "groups", "uname", "hostname", "date", "cal",
		"echo", "printenv", "tree":
		return true
	case "grep":
		// No grep flag executes a command or writes a file (redirections are
		// rejected above).
		return true
	case "rg":
		// --pre/--pre-glob run an arbitrary command.
		return !hasPrefix(args, "--pre")
	case "sed":
		return sedReadOnly(args)
	case "find":
		return findReadOnly(args)
	case "gofmt":
		// -w rewrites; plain gofmt prints, -l lists, -d diffs.
		return !hasAny(args, "-w")
	case "go":
		return goReadOnly(args)
	case "git":
		return gitReadOnly(args)
	case "gh":
		return ghReadOnly(args)
	}
	return false
}

func verifySegment(segment string) bool {
	tokens := stripAssignments(shellWords(segment))
	if len(tokens) == 0 {
		return false
	}
	name := path.Base(strings.ToLower(tokens[0]))
	args := tokens[1:]
	switch name {
	case "make":
		return true
	case "go":
		return len(args) > 0 && oneOf(args[0], "build", "vet", "test")
	case "cargo":
		return len(args) > 0 && oneOf(args[0], "build", "check", "test")
	case "npm", "pnpm", "yarn":
		return len(args) > 0 && oneOf(args[0], "test", "run", "ci")
	case "pytest", "mvn", "gradle", "eslint", "staticcheck", "golangci-lint":
		return true
	case "python", "python3":
		return len(args) >= 2 && args[0] == "-m" && args[1] == "pytest"
	case "tsc":
		return hasAny(args, "--noEmit", "-noEmit")
	}
	return false
}

func sedReadOnly(args []string) bool {
	quiet := false
	var scripts []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-n" || a == "--quiet" || a == "--silent":
			quiet = true
		case a == "-i" || a == "--in-place" || strings.HasPrefix(a, "-i"):
			return false
		case a == "-e" || a == "--expression":
			if i+1 < len(args) {
				scripts = append(scripts, args[i+1])
				i++
			}
		case strings.HasPrefix(a, "-"):
			// -E/-r/-u etc are fine; unknown long forms stay conservative.
			if strings.HasPrefix(a, "--") && a != "--posix" && a != "--regexp-extended" && a != "--unbuffered" {
				return false
			}
		default:
			if len(scripts) == 0 {
				scripts = append(scripts, a)
			}
			// remaining args are files
		}
	}
	if !quiet {
		return false
	}
	for _, script := range scripts {
		// The w/W/e commands write or execute; the s///w flag writes too.
		// Refuse any script that can reach them, even at the cost of an
		// extra prompt. Plain patterns like /error/p stay read-only.
		if sedWriteRe.MatchString(script) {
			return false
		}
	}
	return true
}

var sedWriteRe = regexp.MustCompile(`(^|[;\n}\s|/])[wWe](\s|$)`)

func findReadOnly(args []string) bool {
	for _, a := range args {
		switch a {
		case "-exec", "-execdir", "-ok", "-okdir", "-delete", "-fprint", "-fprint0", "-fprintf", "-fls":
			return false
		}
		if strings.HasPrefix(a, "-fprint") || strings.HasPrefix(a, "-fls") {
			return false
		}
	}
	return true
}

func goReadOnly(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "env":
		// `go env -w` persists settings.
		return !hasAny(args[1:], "-w")
	case "list", "version", "doc":
		return true
	case "mod":
		// graph/why read; download/tidy/vendor write.
		return len(args) > 1 && oneOf(args[1], "graph", "why")
	}
	return false
}

func gitReadOnly(args []string) bool {
	if len(args) == 0 {
		return false
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "status", "diff", "log", "show", "blame", "grep", "describe",
		"shortlog", "reflog", "show-ref", "rev-parse", "ls-files", "ls-tree":
		return true
	case "branch":
		return !hasAny(rest, "-d", "-D", "--delete", "-m", "-M", "--move", "-c", "-C", "--copy")
	case "tag":
		return len(rest) == 0 || hasAny(rest, "-l", "--list", "-n")
	case "stash":
		return len(rest) > 0 && oneOf(rest[0], "list", "show")
	case "remote":
		return len(rest) == 0 || oneOf(rest[0], "-v", "--verbose", "show", "get-url")
	case "config":
		return hasAny(rest, "--get", "--get-all", "--list", "-l")
	}
	return false
}

func ghReadOnly(args []string) bool {
	if len(args) < 2 {
		return false
	}
	switch args[0] {
	case "issue", "pr":
		return oneOf(args[1], "list", "view", "status")
	case "repo":
		return oneOf(args[1], "view")
	case "label":
		return oneOf(args[1], "list")
	case "auth":
		return oneOf(args[1], "status")
	case "run":
		return oneOf(args[1], "list", "view")
	}
	return false
}

// hasWritingRedirection reports a redirect whose target is a file. File
// descriptor duplication (`2>&1`, `>&2`, `2>&-`), /dev/null and stdin from
// /dev/null are allowed.
func hasWritingRedirection(segment string) bool {
	readTarget := func(i int) (string, int) {
		if i < len(segment) && segment[i] == '&' {
			i++
		}
		for i < len(segment) && (segment[i] == ' ' || segment[i] == '\t') {
			i++
		}
		start := i
		for i < len(segment) && segment[i] != ' ' && segment[i] != '\t' && segment[i] != ';' {
			i++
		}
		return segment[start:i], i
	}
	for i := 0; i < len(segment); i++ {
		switch segment[i] {
		case '>':
			j := i + 1
			if j < len(segment) && segment[j] == '>' {
				j++ // append
			}
			target, next := readTarget(j)
			if !safeRedirectTarget(target) {
				return true
			}
			i = next - 1
		case '<':
			target, next := readTarget(i + 1)
			if !safeRedirectTarget(target) {
				return true
			}
			i = next - 1
		}
	}
	return false
}

func safeRedirectTarget(t string) bool {
	switch t {
	case "", "/dev/null", "&1", "&2", "1", "2", "-":
		return true
	}
	return false
}

func stripAssignments(tokens []string) []string {
	for len(tokens) > 0 && isAssignment(tokens[0]) {
		tokens = tokens[1:]
	}
	return tokens
}

func hasAny(args []string, want ...string) bool {
	for _, a := range args {
		for _, w := range want {
			if a == w {
				return true
			}
		}
	}
	return false
}

func hasPrefix(args []string, prefix string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, prefix) {
			return true
		}
	}
	return false
}

func oneOf(v string, options ...string) bool {
	for _, o := range options {
		if v == o {
			return true
		}
	}
	return false
}
