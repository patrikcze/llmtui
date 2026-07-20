package tools

import (
	"path/filepath"
	"regexp"
	"strings"
)

// GuardrailPolicy toggles the workspace tool protections. The zero value is
// unsafe; use DefaultGuardrails (everything on) and let config opt out.
type GuardrailPolicy struct {
	// BlockGitDirWrites rejects write_file into .git (a written hook would
	// execute on the user's next git command).
	BlockGitDirWrites bool
	// BlockSymlinkEscape rejects paths whose symlinks resolve outside the
	// workspace root.
	BlockSymlinkEscape bool
	// ProtectSecretFiles rejects writes into key material directories
	// (.ssh, .gnupg).
	ProtectSecretFiles bool
	// ProtectShellStartupFiles rejects writes to shell startup files
	// (.bashrc, .zshrc, config.fish, …) which would execute on the user's
	// next shell.
	ProtectShellStartupFiles bool
	// RequireApprovalForSecretReads makes read_file (and read-only commands
	// touching such paths) ask before reading likely secret files (.env,
	// *.pem, id_rsa, …).
	RequireApprovalForSecretReads bool
}

// DefaultGuardrails returns the policy with every protection enabled.
func DefaultGuardrails() GuardrailPolicy {
	return GuardrailPolicy{
		BlockGitDirWrites:             true,
		BlockSymlinkEscape:            true,
		ProtectSecretFiles:            true,
		ProtectShellStartupFiles:      true,
		RequireApprovalForSecretReads: true,
	}
}

// CommandVerdict says whether a run_command line may run without approval.
type CommandVerdict string

const (
	// VerdictAuto marks provably read-only commands that run without asking.
	VerdictAuto CommandVerdict = "auto"
	// VerdictAsk marks everything else: the user must approve first.
	VerdictAsk CommandVerdict = "ask"
)

// CommandClass is a classification with a human-readable justification,
// shown by /tools check and usable in approval prompts.
type CommandClass struct {
	Verdict CommandVerdict
	Reason  string
}

// riskyPrograms maps known-dangerous programs to why they need approval.
// Everything not allowlisted needs approval anyway; these just carry a
// better explanation.
var riskyPrograms = map[string]string{
	"rm": "deletes files", "rmdir": "deletes directories",
	"mv": "moves or overwrites files", "cp": "copies over files",
	"chmod": "changes permissions", "chown": "changes ownership",
	"sudo": "privilege escalation", "doas": "privilege escalation",
	"ssh": "remote access", "scp": "remote copy", "sftp": "remote copy",
	"rsync": "can copy to remote hosts and delete files",
	"curl":  "network download", "wget": "network download",
	"brew": "package manager", "apt": "package manager",
	"apt-get": "package manager", "yum": "package manager",
	"dnf": "package manager", "pacman": "package manager",
	"pip": "package manager", "pip3": "package manager",
	"npm": "package manager", "npx": "runs arbitrary packages",
	"yarn": "package manager", "pnpm": "package manager",
	"gem": "package manager", "cargo": "package manager",
	"dd": "raw disk writes", "mkfs": "formats filesystems",
	"kill": "terminates processes", "killall": "terminates processes",
	"aws": "cloud CLI", "gcloud": "cloud CLI", "az": "cloud CLI",
	"kubectl": "cluster CLI", "docker": "container runtime",
}

// autoAllowedGoSubcommands are go toolchain operations treated as safe
// project checks.
var autoAllowedGoSubcommands = map[string]bool{
	"test": true, "vet": true, "fmt": true, "list": true,
	"version": true, "env": true,
}

// ClassifyCommand classifies one run_command line conservatively: only an
// allowlisted read-only program with no shell metacharacters, no escalating
// arguments, and no path argument outside root earns VerdictAuto. Everything
// unknown asks. root is the workspace directory the command will actually
// run in (Runner.root); pass "." when there is no live workspace (e.g. a
// preview with no runner yet).
func (p GuardrailPolicy) ClassifyCommand(body, root string) CommandClass {
	cmdline := strings.TrimSpace(body)
	if cmdline == "" {
		return CommandClass{VerdictAsk, "empty command"}
	}
	if strings.ContainsAny(cmdline, "\n\r") {
		return CommandClass{VerdictAsk, "multiple lines"}
	}
	if strings.ContainsAny(cmdline, "|;&<>`$\\") {
		return CommandClass{VerdictAsk, "shell metacharacters (pipes, redirects, chaining, or substitution)"}
	}
	if strings.ContainsAny(cmdline, "*?[]{}") {
		return CommandClass{VerdictAsk, "shell wildcard expansion requires approval"}
	}
	// Auto-approval must not attempt to partially emulate shell quote removal.
	// Embedded or outer quotes can concatenate tokens into a different logical
	// path at execution time (for example i\"\"d_rsa). A quoted command is still
	// available after explicit approval, but it is never silently executed.
	if strings.ContainsAny(cmdline, "\"'") {
		return CommandClass{VerdictAsk, "quoted arguments require approval"}
	}
	fields := strings.Fields(cmdline)
	prog := fields[0]
	if reason, ok := riskyPrograms[prog]; ok {
		return CommandClass{VerdictAsk, prog + ": " + reason}
	}
	if strings.ContainsAny(prog, "/\\") {
		return CommandClass{VerdictAsk, "explicit program path (not an allowlisted command)"}
	}
	classReason := "allowlisted read-only command"
	switch prog {
	case "git":
		if !gitSubcommandIsReadOnly(fields) {
			return CommandClass{VerdictAsk, "git subcommand can modify the repository"}
		}
		if fields[1] == "diff" {
			for _, f := range fields[2:] {
				if f == "--no-index" {
					return CommandClass{VerdictAsk, "git diff --no-index can read arbitrary filesystem paths"}
				}
			}
		}
		classReason = "read-only git subcommand"
	case "go":
		if len(fields) <= 1 || !autoAllowedGoSubcommands[fields[1]] {
			return CommandClass{VerdictAsk, "go subcommand can modify files or fetch modules"}
		}
		classReason = "go toolchain check"
	default:
		if !autoAllowedCommands[prog] {
			return CommandClass{VerdictAsk, "not an allowlisted read-only command"}
		}
	}
	for _, f := range fields[1:] {
		switch f {
		case "-delete", "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fprintf":
			return CommandClass{VerdictAsk, f + " escalates a read into a write or execution"}
		}
	}
	for _, f := range fields[1:] {
		if strings.HasPrefix(f, "-") {
			continue // a flag, not a path argument
		}
		if looksLikePathEscape(f, root) {
			return CommandClass{VerdictAsk, "argument " + f + " is outside the workspace"}
		}
		if p.BlockSymlinkEscape && pathResolvesOutsideWorkspace(f, root) {
			return CommandClass{VerdictAsk, "argument " + f + " resolves outside the workspace"}
		}
	}
	if p.RequireApprovalForSecretReads {
		for _, f := range fields[1:] {
			if !strings.HasPrefix(f, "-") && IsSecretPath(f) {
				return CommandClass{VerdictAsk, "reads a likely secret file (" + f + ")"}
			}
		}
	}
	return CommandClass{VerdictAuto, classReason}
}

// ClassifyCommand classifies with every protection enabled, using "." as the
// workspace root for callers with no live runner (e.g. tests, or a preview
// before a workspace exists). Runner-backed decisions go through
// (*Runner).NeedsApproval, which passes the runner's real root instead.
func ClassifyCommand(body string) CommandClass {
	return DefaultGuardrails().ClassifyCommand(body, ".")
}

// looksLikePathEscape reports whether argument f, treated as a path relative
// to root (the directory the command actually runs in), would resolve
// outside root. Bare filenames with no separator are never flagged — they
// can only mean "inside root". A "~"-prefixed argument is always flagged:
// home-relative paths are never inside an arbitrary workspace root.
func looksLikePathEscape(f, root string) bool {
	if f == "" {
		return false
	}
	if strings.HasPrefix(f, "~") {
		return true
	}
	// The command may be inspected on one OS and later run on another (for
	// example, tests and release builds). Native filepath.IsAbs alone treats
	// "/etc/hosts" as relative on Windows and "C:/..." as relative on Unix.
	// Drive-relative paths such as "C:notes.txt" are also unsafe because cmd
	// resolves them against process state outside the workspace root.
	if isAbsoluteCommandPath(f) {
		return true
	}
	if !strings.ContainsAny(f, "/\\") && !filepath.IsAbs(f) {
		return false
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	abs := filepath.Clean(filepath.Join(rootAbs, f))
	return abs != rootAbs && !strings.HasPrefix(abs, rootAbs+string(filepath.Separator))
}

func isAbsoluteCommandPath(value string) bool {
	if filepath.IsAbs(value) || strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\`) {
		return true
	}
	return len(value) >= 2 && isASCIILetter(value[0]) && value[1] == ':'
}

func isASCIILetter(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

// pathResolvesOutsideWorkspace checks the deepest existing portion of a
// command argument. This catches both a bare filename symlink and a path to a
// not-yet-existing child below an escaping symlink, matching Runner.resolve.
// Non-path arguments normally have no existing ancestor below root and are
// therefore harmless here.
func pathResolvesOutsideWorkspace(f, root string) bool {
	if f == "" || strings.HasPrefix(f, "-") {
		return false
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	rootResolved, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return false
	}
	candidate := f
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(rootAbs, candidate)
	}
	candidate = filepath.Clean(candidate)
	for {
		resolved, resolveErr := filepath.EvalSymlinks(candidate)
		if resolveErr == nil {
			return resolved != rootResolved && !strings.HasPrefix(resolved, rootResolved+string(filepath.Separator))
		}
		parent := filepath.Dir(candidate)
		if parent == candidate || candidate == rootAbs {
			return false
		}
		candidate = parent
	}
}

// secretNameWords are word-boundary–sensitive credential markers.
// We split the base name on [-_. ] and check each segment to avoid
// matching "tokenizer.go" while still catching "api_secret.json" or
// "db-token.yaml".
var secretNameWords = map[string]bool{
	"password": true, "passwd": true, "secret": true, "token": true,
	"credential": true, "credentials": true, "apikey": true, "api": false,
}

// secretNamePattern is a fallback for unsplit names like "api-key.txt".
var secretNamePattern = regexp.MustCompile(`(?i)(^|[-_. ])(password|passwd|secret|token|credential|credentials|apikey|api.?key)(s?)($|[-_. ])`)

// secretDirs are directories whose contents are key material.
var secretDirs = map[string]bool{".ssh": true, ".gnupg": true}

// IsSecretPath reports whether a path likely holds credentials: .env files,
// key/certificate files, SSH identities, GPG/SSH directories, or
// credential-ish names.
func IsSecretPath(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(filepath.Clean(path)), "/") {
		if secretDirs[strings.ToLower(part)] {
			return true
		}
	}
	base := strings.ToLower(filepath.Base(filepath.ToSlash(path)))
	if base == ".env" || strings.HasPrefix(base, ".env.") {
		return true
	}
	switch filepath.Ext(base) {
	case ".pem", ".key", ".p12", ".pfx", ".jks", ".keystore":
		return true
	}
	for _, id := range []string{"id_rsa", "id_ed25519", "id_ecdsa", "id_dsa"} {
		if base == id || strings.HasPrefix(base, id+".") {
			return true
		}
	}
	if base == ".netrc" || base == ".npmrc" || base == ".pypirc" {
		return true
	}
	// Split on common separators and check each segment.
	// This catches "api_secret.json" while sparing "tokenizer.go".
	nameWithoutExt := strings.TrimSuffix(base, filepath.Ext(base))
	for _, part := range regexp.MustCompile(`[-_. ]+`).Split(nameWithoutExt, -1) {
		if secretNameWords[strings.ToLower(part)] {
			return true
		}
	}
	return secretNamePattern.MatchString(base)
}

// shellStartupFiles are files a shell sources on start; a write here
// executes on the user's next terminal.
var shellStartupFiles = map[string]bool{
	".bashrc": true, ".bash_profile": true, ".bash_login": true,
	".bash_logout": true, ".zshrc": true, ".zshenv": true,
	".zprofile": true, ".zlogin": true, ".zlogout": true,
	".profile": true, ".kshrc": true, "config.fish": true,
}

// IsShellStartupPath reports whether a path is a shell startup file.
func IsShellStartupPath(path string) bool {
	return shellStartupFiles[strings.ToLower(filepath.Base(filepath.ToSlash(path)))]
}

// checkWritePath applies the write guardrails to a workspace-relative path,
// returning a human-readable refusal or "".
func (p GuardrailPolicy) checkWritePath(rel string) string {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(rel)), "/")
	for _, part := range parts {
		if p.BlockGitDirWrites && strings.EqualFold(part, ".git") {
			return "writing inside .git is not allowed"
		}
		if p.ProtectSecretFiles && secretDirs[strings.ToLower(part)] {
			return "writing into " + part + " (key material) is not allowed"
		}
	}
	if p.ProtectShellStartupFiles && IsShellStartupPath(rel) {
		return "writing to shell startup files is not allowed (tools.guardrails.protect_shell_startup_files)"
	}
	return ""
}
