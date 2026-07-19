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
		"git status", "git log --oneline", "git diff HEAD",
		"go test ./...", "go vet ./...", "go fmt ./...", "go list ./...",
	}
	p := DefaultGuardrails()
	for _, cmd := range safe {
		cl := p.ClassifyCommand(cmd, ".")
		if cl.Verdict != VerdictAuto {
			t.Errorf("ClassifyCommand(%q) = %v (%s), want auto", cmd, cl.Verdict, cl.Reason)
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
	cl := p.ClassifyCommand("cat /etc/hosts", root)
	if cl.Verdict != VerdictAsk {
		t.Errorf("cat /etc/hosts = %v (%s), want ask", cl.Verdict, cl.Reason)
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
