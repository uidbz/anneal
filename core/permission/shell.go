package permission

import (
	"os"
	"path/filepath"
	"strings"
)

// Command is one parsed shell command and the prefix an "always" approval
// should save. The splitter is intentionally conservative: compound commands
// yield one entry per segment, so every segment must be allowed.
type Command struct {
	Resource string
	Save     string
}

// ShellScan is the permission-relevant shape of a shell command.
type ShellScan struct {
	Commands    []Command
	Directories []string // cd/pushd targets, in order
}

// ScanCommand splits a shell command into independently authorizeable
// commands and records directory changes. Quoted control operators do not
// split. Commands containing command or process substitution are kept whole,
// because their substituted parts cannot be analyzed safely.
func ScanCommand(command string) ShellScan {
	command = strings.TrimSpace(command)
	if command == "" {
		return ShellScan{}
	}

	segments, substitution := splitSegments(command)
	if substitution {
		segments = []string{command}
	}

	var out ShellScan
	for _, segment := range segments {
		tokens := shellWords(segment)
		if len(tokens) == 0 {
			continue
		}
		name := strings.ToLower(tokens[0])
		if isDirectoryCommand(name) {
			out.Directories = append(out.Directories, directoryArgs(tokens)...)
			continue
		}
		out.Commands = append(out.Commands, Command{
			Resource: segment,
			Save:     savePattern(tokens) + " *",
		})
	}
	return out
}

// Prefix is the command-prefix arity table used to build "always" grants,
// mirrored from OpenCode's shell parser. The value is the number of leading
// tokens that identify the operation ("git commit *", "go test *").
var prefixArity = map[string]int{
	"cat": 1, "cd": 1, "chmod": 1, "chown": 1, "cp": 1, "echo": 1, "env": 1,
	"export": 1, "grep": 1, "kill": 1, "killall": 1, "ln": 1, "ls": 1,
	"mkdir": 1, "mv": 1, "ps": 1, "pwd": 1, "rm": 1, "rmdir": 1, "sleep": 1,
	"source": 1, "tail": 1, "touch": 1, "unset": 1, "which": 1,

	"aws": 3, "az": 3, "bazel": 2, "brew": 2, "bun": 2, "bun run": 3,
	"bun x": 3, "cargo": 2, "cargo add": 3, "cargo run": 3, "cdk": 2,
	"cf": 2, "cmake": 2, "composer": 2, "consul": 2, "consul kv": 3,
	"crictl": 2, "deno": 2, "deno task": 3, "doctl": 3, "docker": 2,
	"docker builder": 3, "docker compose": 3, "docker container": 3,
	"docker image": 3, "docker network": 3, "docker volume": 3, "eksctl": 2,
	"eksctl create": 3, "firebase": 2, "flyctl": 2, "gcloud": 3, "gh": 3,
	"git": 2, "git config": 3, "git remote": 3, "git stash": 3, "go": 2,
	"gradle": 2, "helm": 2, "heroku": 2, "hugo": 2, "ip": 2, "ip addr": 3,
	"ip link": 3, "ip netns": 3, "ip route": 3, "kind": 2, "kind create": 3,
	"kubectl": 2, "kubectl kustomize": 3, "kubectl rollout": 3, "kustomize": 2,
	"make": 2, "mc": 2, "mc admin": 3, "minikube": 2, "mongosh": 2, "mysql": 2,
	"mvn": 2, "ng": 2, "npm": 2, "npm exec": 3, "npm init": 3, "npm run": 3,
	"npm view": 3, "npx": 2, "nvm": 2, "nx": 2, "openssl": 2,
	"openssl req": 3, "openssl x509": 3, "pip": 2, "pipenv": 2, "pnpm": 2,
	"pnpm dlx": 3, "pnpm exec": 3, "pnpm run": 3, "poetry": 2, "podman": 2,
	"podman container": 3, "podman image": 3, "psql": 2, "pulumi": 2,
	"pulumi stack": 3, "python": 2, "pyenv": 2, "rake": 2, "rbenv": 2,
	"redis-cli": 2, "rustup": 2, "serverless": 2, "sfdx": 3, "skaffold": 2,
	"sls": 2, "sst": 2, "swift": 2, "systemctl": 2, "terraform": 2,
	"terraform workspace": 3, "tmux": 2, "turbo": 2, "ufw": 2, "vault": 2,
	"vault auth": 3, "vault kv": 3, "vercel": 2, "volta": 2, "wp": 2,
	"yarn": 2, "yarn dlx": 3, "yarn run": 3,
}

func savePattern(tokens []string) string {
	// Environment assignments are not part of the operation name.
	for len(tokens) > 0 && isAssignment(tokens[0]) {
		tokens = tokens[1:]
	}
	if len(tokens) == 0 {
		return ""
	}
	max := len(tokens)
	if max > 3 {
		max = 3
	}
	for length := max; length > 0; length-- {
		if arity, ok := prefixArity[strings.Join(tokens[:length], " ")]; ok {
			if arity < len(tokens) {
				return strings.Join(tokens[:arity], " ")
			}
			return strings.Join(tokens, " ")
		}
	}
	return tokens[0]
}

func isAssignment(token string) bool {
	i := strings.IndexByte(token, '=')
	if i <= 0 {
		return false
	}
	name := token[:i]
	for j, r := range name {
		if r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || (j > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

func isDirectoryCommand(name string) bool {
	switch name {
	case "cd", "chdir", "popd", "pushd", "push-location", "set-location":
		return true
	}
	return false
}

func directoryArgs(tokens []string) []string {
	if len(tokens) < 2 {
		if home, err := os.UserHomeDir(); err == nil {
			return []string{home}
		}
		return nil
	}
	var dirs []string
	for _, token := range tokens[1:] {
		if token == "" || token == "-" || strings.HasPrefix(token, "-") {
			continue
		}
		// Unknown shell expressions cannot be resolved without executing them.
		if strings.ContainsAny(token, "$`") || strings.HasPrefix(token, "(") {
			continue
		}
		dirs = append(dirs, ExpandHome(token))
	}
	return dirs
}

// shellWords splits a segment into words, removing quotes and interpreting
// backslash escapes. It is only used for command-prefix selection, not for
// deciding what executes.
func shellWords(segment string) []string {
	var words []string
	var cur strings.Builder
	inSingle, inDouble := false, false
	started := false
	flush := func() {
		if started {
			words = append(words, cur.String())
			cur.Reset()
			started = false
		}
	}
	for i := 0; i < len(segment); i++ {
		c := segment[i]
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			} else {
				cur.WriteByte(c)
			}
			started = true
		case c == '\\' && i+1 < len(segment):
			i++
			cur.WriteByte(segment[i])
			started = true
		case c == '\'' && !inDouble:
			inSingle = true
			started = true
		case c == '"' && !inSingle:
			inDouble = !inDouble
			started = true
		case (c == ' ' || c == '\t' || c == '\r') && !inDouble:
			flush()
		default:
			cur.WriteByte(c)
			started = true
		}
	}
	flush()
	return words
}

// splitSegments splits on unquoted ;, &&, ||, |, &, and newlines. It reports
// whether the command contains command or process substitution outside single
// quotes, which cannot be analyzed by pattern matching.
func splitSegments(command string) (segments []string, hasSubstitution bool) {
	var cur strings.Builder
	inSingle, inDouble := false, false
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			segments = append(segments, s)
		}
		cur.Reset()
	}

	for i := 0; i < len(command); i++ {
		c := command[i]
		if inSingle {
			if c == '\'' {
				inSingle = false
			}
			cur.WriteByte(c)
			continue
		}
		if c == '\\' && i+1 < len(command) {
			cur.WriteByte(c)
			i++
			cur.WriteByte(command[i])
			continue
		}
		switch c {
		case '\'':
			if !inDouble {
				inSingle = true
			}
			cur.WriteByte(c)
		case '"':
			inDouble = !inDouble
			cur.WriteByte(c)
		case '`':
			hasSubstitution = true
			cur.WriteByte(c)
		case '$':
			if i+1 < len(command) && command[i+1] == '(' {
				hasSubstitution = true
			}
			cur.WriteByte(c)
		case '<', '>':
			if !inDouble && i+1 < len(command) && command[i+1] == '(' {
				hasSubstitution = true
			}
			cur.WriteByte(c)
		case ';', '\n':
			if inDouble {
				cur.WriteByte(c)
				continue
			}
			flush()
		case '&', '|':
			if inDouble {
				cur.WriteByte(c)
				continue
			}
			flush()
			if i+1 < len(command) && command[i+1] == c {
				i++
			}
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return segments, hasSubstitution
}

// ResolveDirectory expands a directory argument relative to base.
func ResolveDirectory(base, dir string) string {
	dir = ExpandHome(dir)
	if filepath.IsAbs(dir) {
		return filepath.Clean(dir)
	}
	return filepath.Clean(filepath.Join(base, dir))
}
