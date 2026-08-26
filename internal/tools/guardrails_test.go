package tools

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// ---- ClassifyCommand -------------------------------------------------------

func TestSafeCommands(t *testing.T) {
	safe := []string{
		"ls", "ls -la", "cat README.md", "head -20 main.go",
		"tail -5 server.log", "grep -rn TODO .", "rg pattern src/",
		"wc -l main.go", "pwd", "find . -name main.go",
		"git status", "git log --oneline", "git diff HEAD", "git show HEAD",
		"go list ./...", "go version", "go env GOPROXY",
	}
	p := DefaultGuardrails()
	for _, cmd := range safe {
		cl := p.ClassifyCommand(cmd, ".")
		if cl.Verdict != VerdictAuto {
			t.Errorf("ClassifyCommand(%q) = %v (%s), want auto", cmd, cl.Verdict, cl.Reason)
		}
	}
}

// TestGoCommandsThatExecuteOrMutateAsk locks in the fix for SEC-001: go
// subcommands that compile/execute repository code (test, vet) or write
// files/config (fmt, env -w/-u) must ask, not auto-approve. These were
// previously (incorrectly) asserted as VerdictAuto in TestSafeCommands.
func TestGoCommandsThatExecuteOrMutateAsk(t *testing.T) {
	cases := []string{
		"go test ./...",
		"go vet ./...",
		"go fmt ./...",
		"go env -w GOPROXY=x",
		"go env -u GOPROXY",
	}
	p := DefaultGuardrails()
	for _, cmd := range cases {
		cl := p.ClassifyCommand(cmd, ".")
		if cl.Verdict != VerdictAsk {
			t.Errorf("ClassifyCommand(%q) = %v (%s), want ask", cmd, cl.Verdict, cl.Reason)
		}
	}
}

func TestRiskyCommands(t *testing.T) {
	risky := []string{
		"rm -rf .", "rm file.txt",
		"mv src dst",
		"curl https://example.com",
		"wget http://example.com",
		"sudo apt-get install foo",
		"npm install",
		"docker run ubuntu",
		"aws s3 ls",
		"kubectl get pods",
		"git push origin main",
		"git commit -am 'save'",
		"go build ./...",
		"chmod +x script.sh",
	}
	p := DefaultGuardrails()
	for _, cmd := range risky {
		cl := p.ClassifyCommand(cmd, ".")
		if cl.Verdict != VerdictAsk {
			t.Errorf("ClassifyCommand(%q) = %v (%s), want ask", cmd, cl.Verdict, cl.Reason)
		}
	}
}

func TestShellMetacharactersAlwaysAsk(t *testing.T) {
	meta := []string{
		"ls | grep go",
		"cat file; rm file",
		"echo hi && rm -rf .",
		"cat file > out.txt",
		"curl http://x.com > /tmp/f",
		"ls `pwd`",
		"echo $HOME",
		"rg --pr%COMSPEC:~0,0%e=calc needle .",
		"rg --pr^e=calc needle .",
		"echo !HOME!",
		"(dir)",
		"cat *.txt",
		"cat n[otes].txt",
	}
	p := DefaultGuardrails()
	for _, cmd := range meta {
		cl := p.ClassifyCommand(cmd, ".")
		if cl.Verdict != VerdictAsk {
			t.Errorf("ClassifyCommand(%q) = %v, want ask (shell metacharacter)", cmd, cl.Verdict)
		}
	}
}

func TestFindEscalatingArgsAsk(t *testing.T) {
	cases := []string{
		"find . -delete",
		"find . -exec rm {} \\;",
		"find . -execdir ls {} \\;",
	}
	p := DefaultGuardrails()
	for _, cmd := range cases {
		cl := p.ClassifyCommand(cmd, ".")
		if cl.Verdict != VerdictAsk {
			t.Errorf("ClassifyCommand(%q) = %v, want ask", cmd, cl.Verdict)
		}
	}
}

func TestClassifyCommandRipgrepHelperOptionsAsk(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
	}{
		{name: "pre equals", cmd: "rg --pre=sh needle payload.sh"},
		{name: "pre separate", cmd: "rg --pre sh needle payload.sh"},
		{name: "pre after operands", cmd: "rg needle payload.sh --pre=sh"},
		{name: "pre reenabled", cmd: "rg --no-pre --pre=sh needle payload.sh"},
		{name: "workspace preprocessor", cmd: "rg --pre=./scripts/filter needle docs"},
		{name: "hostname helper equals", cmd: "rg --hostname-bin=sh --hyperlink-format=default needle ."},
		{name: "hostname helper separate", cmd: "rg --hostname-bin sh --hyperlink-format=default needle ."},
		{name: "search zip long", cmd: "rg --search-zip needle archive.gz"},
		{name: "search zip short", cmd: "rg -z needle archive.gz"},
		{name: "search zip clustered", cmd: "rg -nz needle archive.gz"},
	}
	p := DefaultGuardrails()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl := p.ClassifyCommand(tt.cmd, ".")
			if cl.Verdict != VerdictAsk {
				t.Errorf("ClassifyCommand(%q) = %v (%s), want ask", tt.cmd, cl.Verdict, cl.Reason)
			}
		})
	}
}

func TestClassifyCommandRipgrepObservationalOptionsRemainAuto(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
	}{
		{name: "simple search", cmd: "rg pattern src/"},
		{name: "line numbers", cmd: "rg -n pattern ."},
		{name: "list files", cmd: "rg --files src/"},
		{name: "explicit pattern", cmd: "rg -e TODO internal"},
		{name: "attached pattern", cmd: "rg -ez docs/security.md"},
		{name: "cluster before attached pattern", cmd: "rg -nez docs/security.md"},
		{name: "pattern file", cmd: "rg -f patterns.txt src/"},
		{name: "pre disabled", cmd: "rg --no-pre pattern src/"},
		{name: "literal pre token", cmd: "rg -- --pre=sh"},
	}
	p := DefaultGuardrails()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl := p.ClassifyCommand(tt.cmd, ".")
			if cl.Verdict != VerdictAuto {
				t.Errorf("ClassifyCommand(%q) = %v (%s), want auto", tt.cmd, cl.Verdict, cl.Reason)
			}
		})
	}
}

func TestEmptyCommandAsks(t *testing.T) {
	p := DefaultGuardrails()
	cl := p.ClassifyCommand("", ".")
	if cl.Verdict != VerdictAsk {
		t.Errorf("empty command got %v, want ask", cl.Verdict)
	}
}

// ---- IsSecretPath ----------------------------------------------------------

func TestSecretPaths(t *testing.T) {
	secrets := []string{
		".env", ".env.local", ".env.production",
		"config/server.key", "cert.pem", "client.p12",
		"id_rsa", "id_ed25519", "id_rsa.pub",
		".ssh/config", ".ssh/id_rsa",
		".gnupg/private-keys-v1.d/abc.key",
		".netrc", ".npmrc", ".pypirc",
		"db-password.yaml", "api_secret.json",
	}
	for _, p := range secrets {
		if !IsSecretPath(p) {
			t.Errorf("IsSecretPath(%q) = false, want true", p)
		}
	}
}

func TestNonSecretPaths(t *testing.T) {
	ok := []string{
		"main.go", "internal/tools/tools.go",
		"README.md", "config.yaml",
		"tokenizer.go", // "token" in the middle of a word — boundary should exclude it
		"provider/openai.go",
	}
	for _, p := range ok {
		if IsSecretPath(p) {
			t.Errorf("IsSecretPath(%q) = true, want false", p)
		}
	}
}

// ---- IsShellStartupPath ----------------------------------------------------

func TestShellStartupPaths(t *testing.T) {
	startup := []string{
		".bashrc", ".bash_profile", ".bash_login", ".bash_logout",
		".zshrc", ".zshenv", ".zprofile", ".zlogin", ".zlogout",
		".profile", ".kshrc", "config.fish",
		"/home/user/.zshrc", "/Users/me/.bashrc",
	}
	for _, p := range startup {
		if !IsShellStartupPath(p) {
			t.Errorf("IsShellStartupPath(%q) = false, want true", p)
		}
	}
}

func TestNonStartupPaths(t *testing.T) {
	ok := []string{"main.go", ".env", "config.yaml", ".gitignore"}
	for _, p := range ok {
		if IsShellStartupPath(p) {
			t.Errorf("IsShellStartupPath(%q) = true, want false", p)
		}
	}
}

// ---- SecretRead approval in ClassifyCommand --------------------------------

func TestSecretReadAskWithPolicy(t *testing.T) {
	p := DefaultGuardrails()
	// cat .env should ask when RequireApprovalForSecretReads = true.
	cl := p.ClassifyCommand("cat .env", ".")
	if cl.Verdict != VerdictAsk {
		t.Errorf("cat .env with RequireApprovalForSecretReads = %v, want ask", cl.Verdict)
	}
}

func TestSecretReadAutoWithPolicyOff(t *testing.T) {
	p := DefaultGuardrails()
	p.RequireApprovalForSecretReads = false
	cl := p.ClassifyCommand("cat README.md", ".")
	if cl.Verdict != VerdictAuto {
		t.Errorf("cat README.md = %v, want auto", cl.Verdict)
	}
}

func TestClassifyCommandQuotedSecretPathStillAsks(t *testing.T) {
	p := DefaultGuardrails()
	for _, cmd := range []string{`cat ".env"`, `cat 'id_rsa'`, `cat i""d_rsa`, `cat 'i'd_rsa`, `cat .e""nv`} {
		cl := p.ClassifyCommand(cmd, ".")
		if cl.Verdict != VerdictAsk {
			t.Errorf("%s = %v (%s), want ask (quoting must not bypass secret detection)", cmd, cl.Verdict, cl.Reason)
		}
	}
}

func TestClassifyCommandQuotedPathEscapeStillAsks(t *testing.T) {
	p := DefaultGuardrails()
	root := t.TempDir()
	cl := p.ClassifyCommand(`cat "/etc/hosts"`, root)
	if cl.Verdict != VerdictAsk {
		t.Errorf(`cat "/etc/hosts" = %v (%s), want ask (quoting must not bypass path confinement)`, cl.Verdict, cl.Reason)
	}
}

// ---- run_command path confinement ------------------------------------------

func TestClassifyCommandRejectsAbsolutePathArgument(t *testing.T) {
	p := DefaultGuardrails()
	root := t.TempDir()
	for _, command := range []string{
		"cat /etc/hosts",
		"cat C:/Windows/System32/drivers/etc/hosts",
		"cat C:notes.txt",
	} {
		cl := p.ClassifyCommand(command, root)
		if cl.Verdict != VerdictAsk {
			t.Errorf("%s = %v (%s), want ask", command, cl.Verdict, cl.Reason)
		}
	}
}

func TestAbsoluteCommandPathUsesPortableSyntax(t *testing.T) {
	for _, value := range []string{
		"/etc/hosts",
		`\Windows\System32\drivers\etc\hosts`,
		"C:/Windows/System32/drivers/etc/hosts",
		`C:\Windows\System32\drivers\etc\hosts`,
		"C:notes.txt",
		`\\server\share\notes.txt`,
	} {
		if !isAbsoluteCommandPath(value) {
			t.Errorf("isAbsoluteCommandPath(%q) = false", value)
		}
	}
	for _, value := range []string{"notes.txt", "docs/notes.txt", "./notes.txt"} {
		if isAbsoluteCommandPath(value) {
			t.Errorf("isAbsoluteCommandPath(%q) = true", value)
		}
	}
}

func TestClassifyCommandRejectsParentEscape(t *testing.T) {
	p := DefaultGuardrails()
	root := t.TempDir()
	cl := p.ClassifyCommand("cat ../../outside.txt", root)
	if cl.Verdict != VerdictAsk {
		t.Errorf("cat ../../outside.txt = %v (%s), want ask", cl.Verdict, cl.Reason)
	}
}

func TestClassifyCommandRejectsHomeRelativeArgument(t *testing.T) {
	p := DefaultGuardrails()
	root := t.TempDir()
	cl := p.ClassifyCommand("cat ~/.docker/config.json", root)
	if cl.Verdict != VerdictAsk {
		t.Errorf("cat ~/.docker/config.json = %v (%s), want ask", cl.Verdict, cl.Reason)
	}
}

func TestClassifyCommandAllowsInWorkspacePath(t *testing.T) {
	p := DefaultGuardrails()
	root := t.TempDir()
	cl := p.ClassifyCommand("cat sub/dir/file.go", root)
	if cl.Verdict != VerdictAuto {
		t.Errorf("cat sub/dir/file.go (inside workspace) = %v (%s), want auto", cl.Verdict, cl.Reason)
	}
}

func TestClassifyCommandBareSymlinkEscapeAsks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "notes.txt")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	p := DefaultGuardrails()
	cl := p.ClassifyCommand("cat notes.txt", root)
	if cl.Verdict != VerdictAsk {
		t.Fatalf("cat escaping symlink = %v (%s), want ask", cl.Verdict, cl.Reason)
	}
}

func TestClassifyCommandInWorkspaceSymlinkIsAuto(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	root := t.TempDir()
	target := filepath.Join(root, "target.txt")
	if err := os.WriteFile(target, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "notes.txt")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	p := DefaultGuardrails()
	cl := p.ClassifyCommand("cat notes.txt", root)
	if cl.Verdict != VerdictAuto {
		t.Fatalf("cat in-workspace symlink = %v (%s), want auto", cl.Verdict, cl.Reason)
	}
}

// ---- git subcommand classification -----------------------------------------

func TestClassifyCommandGitBranchDeleteAsks(t *testing.T) {
	p := DefaultGuardrails()
	cl := p.ClassifyCommand("git branch -D main", ".")
	if cl.Verdict != VerdictAsk {
		t.Errorf("git branch -D main = %v (%s), want ask", cl.Verdict, cl.Reason)
	}
}

func TestClassifyCommandGitRemoteAddAsks(t *testing.T) {
	p := DefaultGuardrails()
	cl := p.ClassifyCommand("git remote add evil http://attacker.example/repo.git", ".")
	if cl.Verdict != VerdictAsk {
		t.Errorf("git remote add ... = %v (%s), want ask", cl.Verdict, cl.Reason)
	}
}

func TestClassifyCommandGitRemoteSetURLAsks(t *testing.T) {
	p := DefaultGuardrails()
	cl := p.ClassifyCommand("git remote set-url origin http://attacker.example/repo.git", ".")
	if cl.Verdict != VerdictAsk {
		t.Errorf("git remote set-url ... = %v (%s), want ask", cl.Verdict, cl.Reason)
	}
}

func TestClassifyCommandGitBranchBareIsAuto(t *testing.T) {
	p := DefaultGuardrails()
	cl := p.ClassifyCommand("git branch", ".")
	if cl.Verdict != VerdictAuto {
		t.Errorf("git branch = %v (%s), want auto", cl.Verdict, cl.Reason)
	}
}

func TestClassifyCommandGitRemoteListIsAuto(t *testing.T) {
	p := DefaultGuardrails()
	cl := p.ClassifyCommand("git remote -v", ".")
	if cl.Verdict != VerdictAuto {
		t.Errorf("git remote -v = %v (%s), want auto", cl.Verdict, cl.Reason)
	}
}

func TestClassifyCommandGitNoIndexAndSensitivePathsAsk(t *testing.T) {
	p := DefaultGuardrails()
	root := t.TempDir()
	for _, cmd := range []string{
		"git diff --no-index empty.txt /etc/passwd",
		"git show id_rsa",
		"git log .env",
		"git blame ../outside.txt",
	} {
		cl := p.ClassifyCommand(cmd, root)
		if cl.Verdict != VerdictAsk {
			t.Errorf("%q = %v (%s), want ask", cmd, cl.Verdict, cl.Reason)
		}
	}
}

func TestClassifyCommandGitDiffShowOutputAsks(t *testing.T) {
	p := DefaultGuardrails()
	for _, cmd := range []string{
		"git diff --output=result.txt",
		"git diff --output result.txt",
		"git diff -o result.txt",
		"git diff -oresult.txt",
		"git show --output=result.txt",
		"git show --output result.txt",
		"git show -o result.txt",
	} {
		cl := p.ClassifyCommand(cmd, ".")
		if cl.Verdict != VerdictAsk {
			t.Errorf("%q = %v (%s), want ask", cmd, cl.Verdict, cl.Reason)
		}
	}
}

func TestFindWriteOutputEscalatingArgsAsk(t *testing.T) {
	cases := []string{
		"find . -fls result.txt",
		"find . -fprint0 result.txt",
	}
	p := DefaultGuardrails()
	for _, cmd := range cases {
		cl := p.ClassifyCommand(cmd, ".")
		if cl.Verdict != VerdictAsk {
			t.Errorf("ClassifyCommand(%q) = %v (%s), want ask", cmd, cl.Verdict, cl.Reason)
		}
	}
}

// ---- flag-value path smuggling ---------------------------------------------

// TestFlagValuePathSmugglingAsks locks in the fix for the gap where a path
// embedded in a flag's value ("--file=/etc/passwd") skipped the path-escape
// check entirely because the argument starts with "-". The fix lives in the
// shared per-argument loop (flagPathValue), not as a per-program special
// case, so both grep and cat (an unrelated program) must be caught the same
// way.
func TestFlagValuePathSmugglingAsks(t *testing.T) {
	p := DefaultGuardrails()
	root := t.TempDir()
	for _, cmd := range []string{
		"grep --file=/absolute/path x .",
		"cat --file=/absolute/path",
		"cat --output=/etc/passwd",
	} {
		cl := p.ClassifyCommand(cmd, root)
		if cl.Verdict != VerdictAsk {
			t.Errorf("%q = %v (%s), want ask", cmd, cl.Verdict, cl.Reason)
		}
	}
}

// ---- adversarial verdict table (SEC-001) -----------------------------------

// TestCommandApprovalAdversarialTable asserts the exact verdict table from
// the SEC-001 remediation brief / .claude/tasks/llmtui-test-plan.md
// §"Command approval adversarial table".
func TestCommandApprovalAdversarialTable(t *testing.T) {
	cases := []struct {
		cmd  string
		want CommandVerdict
	}{
		// Must ask: executes repository code, mutates state, or smuggles a
		// path past the naive "skip anything starting with -" check.
		{"go test ./...", VerdictAsk},
		{"go vet ./...", VerdictAsk},
		{"go fmt ./...", VerdictAsk},
		{"go env -w GOPROXY=x", VerdictAsk},
		{"go env -u GOPROXY", VerdictAsk},
		{"git diff --output=result.txt", VerdictAsk},
		{"git show --output=result.txt", VerdictAsk},
		{"find . -fls result.txt", VerdictAsk},
		{"grep --file=/absolute/path x .", VerdictAsk},
		{"cat --file=/absolute/path", VerdictAsk},

		// Must already ask: regression tests locking in existing behavior.
		{"cat -- /absolute/path", VerdictAsk},
		{"go env -unknownflag", VerdictAsk},
		{"go env @file", VerdictAsk},

		// Must remain auto: do not regress currently-safe forms.
		{"ls", VerdictAuto},
		{"ls -la", VerdictAuto},
		{"cat README.md", VerdictAuto},
		{"head -20 main.go", VerdictAuto},
		{"grep -rn TODO .", VerdictAuto},
		{"find . -name main.go", VerdictAuto},
		{"git status", VerdictAuto},
		{"git log --oneline", VerdictAuto},
		{"git diff HEAD", VerdictAuto},
		{"git show HEAD", VerdictAuto},
		{"go list ./...", VerdictAuto},
		{"go version", VerdictAuto},
		{"go env GOPROXY", VerdictAuto},
	}
	p := DefaultGuardrails()
	root := t.TempDir()
	for _, c := range cases {
		cl := p.ClassifyCommand(c.cmd, root)
		if cl.Verdict != c.want {
			t.Errorf("ClassifyCommand(%q) = %v (%s), want %v", c.cmd, cl.Verdict, cl.Reason, c.want)
		}
	}
}

// ---- fuzz-style: unrecognized suffixes never flip ask->auto or auto->ask'd-then-auto ----

// TestUnrecognizedSuffixNeverBecomesAuto is the regression form of the
// CLAUDE.md invariant: appending an arbitrary/unrecognized token to a
// currently-auto command must never turn it into VerdictAuto unless that
// exact new form has an explicit recognizer. Every case here is expected to
// ask precisely because the appended token is not (yet) understood.
func TestUnrecognizedSuffixNeverBecomesAuto(t *testing.T) {
	cases := []string{
		"go env -unknownflag",
		"go env GOPROXY -w",
		"go env GOPROXY=x",      // not a bare KEY token
		"go env lowercase",      // not KEY-shaped
		"go test -run TestFoo",  // still go test, still asks
		"go vet -unsafeptr",     // still go vet, still asks
		"go fmt -n",             // still go fmt, still asks
		"go banana",             // unknown go subcommand
		"git diff --output",     // flag alone, no "="
		"git show -oresult.txt", // attached-value short flag
		"find . -fprint0 x",
		"find . -fls x",
	}
	p := DefaultGuardrails()
	for _, cmd := range cases {
		cl := p.ClassifyCommand(cmd, ".")
		if cl.Verdict != VerdictAsk {
			t.Errorf("ClassifyCommand(%q) = %v (%s), want ask (unrecognized/escalating form must not auto-approve)", cmd, cl.Verdict, cl.Reason)
		}
	}
}
