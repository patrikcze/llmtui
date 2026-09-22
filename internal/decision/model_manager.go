package decision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	modelManifestName = "LLMTUI_DECISION_MANIFEST.json"
	modelSchema       = 1
	maxModelFileBytes = int64(4 << 30)
)

// ModelManagerOptions configures source-model acquisition. A runtime artifact
// is not inferred from SafeTensors: the manifest records that the downloaded
// source model is not executable until a verified backend is supplied.
type ModelManagerOptions struct {
	RootDir   string
	Endpoint  string
	Client    *http.Client
	Catalog   []ModelDescriptor
	Token     string
	AllowHTTP bool // intended only for loopback httptest endpoints
}

// Progress describes one artifact's bounded download progress.
type Progress struct {
	Model      string
	Artifact   string
	Downloaded int64
	Total      int64
	Done       bool
}

// PullOptions controls one explicit model installation.
type PullOptions struct {
	Revision string
	Repair   bool
	Progress func(Progress)
}

// ArtifactManifest records the exact bytes installed for one source file.
type ArtifactManifest struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// ModelManifest separates upstream source identity from the future executable
// runtime representation. The current implementation deliberately publishes
// runtime_ready=false for SafeTensors-only installations.
type ModelManifest struct {
	SchemaVersion int                `json:"schema_version"`
	Engine        string             `json:"engine"`
	Model         string             `json:"model"`
	Source        ModelSource        `json:"source"`
	Runtime       RuntimeArtifact    `json:"runtime"`
	Files         []ArtifactManifest `json:"files"`
	InstalledAt   string             `json:"installed_at"`
}

type ModelSource struct {
	Type       string `json:"type"`
	Repository string `json:"repo"`
	Revision   string `json:"revision"`
}

type RuntimeArtifact struct {
	Format           string `json:"format"`
	Ready            bool   `json:"ready"`
	Path             string `json:"path,omitempty"`
	Size             int64  `json:"size,omitempty"`
	Exporter         string `json:"exporter,omitempty"`
	UpstreamRevision string `json:"upstream_revision,omitempty"`
	SHA256           string `json:"sha256,omitempty"`
	Note             string `json:"note,omitempty"`
}

// Installation is a discovered or newly installed model directory.
type Installation struct {
	ID       string
	Path     string
	Manifest ModelManifest
	Valid    bool
}

// ModelManager owns the local source-model store and its HTTP boundary.
type ModelManager struct {
	root     string
	endpoint *url.URL
	client   *http.Client
	token    string
	catalog  []ModelDescriptor

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewModelManager validates all paths and descriptors before any network or
// filesystem operation. RootDir is normally the platform data directory.
func NewModelManager(opts ModelManagerOptions) (*ModelManager, error) {
	root := opts.RootDir
	if root == "" {
		var err error
		root, err = defaultModelRoot()
		if err != nil {
			return nil, err
		}
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve model root: %w", err)
	}
	endpoint := opts.Endpoint
	if endpoint == "" {
		endpoint = "https://huggingface.co"
	}
	u, err := url.Parse(strings.TrimRight(endpoint, "/"))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid model endpoint %q", endpoint)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("model endpoint must not contain credentials, query, or fragment")
	}
	if u.Scheme != "https" && (!opts.AllowHTTP || !isLoopbackHost(u.Hostname())) {
		return nil, fmt.Errorf("model endpoint must use HTTPS")
	}
	catalog := opts.Catalog
	if len(catalog) == 0 {
		catalog = defaultModelCatalog()
	}
	seen := make(map[string]struct{}, len(catalog))
	for _, descriptor := range catalog {
		if err := validateDescriptor(descriptor); err != nil {
			return nil, err
		}
		if _, ok := seen[descriptor.Alias]; ok {
			return nil, fmt.Errorf("duplicate model alias %q", descriptor.Alias)
		}
		seen[descriptor.Alias] = struct{}{}
	}
	client := opts.Client
	if client == nil {
		client = &http.Client{}
	}
	// Hugging Face may redirect large objects to a CDN. Stay on the same
	// configured host and scheme; a model endpoint must never become an
	// arbitrary URL fetcher.
	baseClient := *client
	baseRedirect := baseClient.CheckRedirect
	baseClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if baseRedirect != nil {
			if err := baseRedirect(req, via); err != nil {
				return err
			}
		}
		if req.URL.Scheme != "https" && (u.Scheme != "http" || !isLoopbackHost(req.URL.Hostname())) {
			return fmt.Errorf("refusing insecure model redirect to %q", req.URL.Redacted())
		}
		if !allowedModelRedirectHost(req.URL.Hostname(), u.Hostname()) {
			return fmt.Errorf("refusing model redirect to %q", req.URL.Redacted())
		}
		return nil
	}
	client = &baseClient
	token := opts.Token
	if token == "" {
		token = os.Getenv("HF_TOKEN")
	}
	return &ModelManager{
		root: root, endpoint: u, client: client, token: token,
		catalog: catalog, locks: make(map[string]*sync.Mutex),
	}, nil
}

func defaultModelRoot() (string, error) {
	var base string
	switch runtime.GOOS {
	case "windows":
		base = os.Getenv("LOCALAPPDATA")
		if base == "" {
			return "", errors.New("LOCALAPPDATA is not set")
		}
	default:
		base = os.Getenv("XDG_DATA_HOME")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("resolve home directory: %w", err)
			}
			base = filepath.Join(home, ".local", "share")
		}
	}
	return filepath.Join(base, "llmtui", "models", "laya"), nil
}

func (m *ModelManager) Catalog() []ModelDescriptor {
	return append([]ModelDescriptor(nil), m.catalog...)
}

func (m *ModelManager) descriptor(id string) (ModelDescriptor, error) {
	return descriptorForID(id, m.catalog)
}

func (m *ModelManager) modelLock(alias string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	lock := m.locks[alias]
	if lock == nil {
		lock = &sync.Mutex{}
		m.locks[alias] = lock
	}
	return lock
}

type hubModelInfo struct {
	SHA string `json:"sha"`
}

type hubTreeEntry struct {
	Path string  `json:"path"`
	Type string  `json:"type"`
	Size int64   `json:"size"`
	LFS  *hubLFS `json:"lfs,omitempty"`
}

type hubLFS struct {
	SHA256 string `json:"sha256"`
}

type artifact struct {
	Path   string
	Size   int64
	SHA256 string
	URL    string
}

// Pull resolves, downloads, verifies, and atomically installs one checkpoint.
// A failed download leaves only private staging/partial files; no manifest is
// written at the final path, so incomplete data cannot look installed.
func (m *ModelManager) Pull(ctx context.Context, id string, opts PullOptions) (Installation, error) {
	descriptor, err := m.descriptor(id)
	if err != nil {
		return Installation{}, err
	}
	if err := ctx.Err(); err != nil {
		return Installation{}, err
	}
	lock := m.modelLock(descriptor.Alias)
	lock.Lock()
	defer lock.Unlock()

	if err := os.MkdirAll(filepath.Join(m.root, descriptor.Alias), 0o700); err != nil {
		return Installation{}, fmt.Errorf("create model directory: %w", err)
	}
	revision, err := m.resolveRevision(ctx, descriptor, opts.Revision)
	if err != nil {
		return Installation{}, err
	}
	if !safeRevision(revision) {
		return Installation{}, fmt.Errorf("unsafe resolved revision %q", revision)
	}
	finalDir := filepath.Join(m.root, descriptor.Alias, revision)
	if existing, err := m.inspectPath(finalDir, descriptor, true); err == nil && existing.Valid {
		return existing, nil
	}
	if info, statErr := os.Stat(finalDir); statErr == nil && !info.IsDir() {
		return Installation{}, fmt.Errorf("model destination %q is not a directory", finalDir)
	}
	if _, statErr := os.Stat(finalDir); statErr == nil && !opts.Repair {
		return Installation{}, fmt.Errorf("model destination %q exists but is invalid; rerun with repair enabled", finalDir)
	}
	if opts.Repair {
		if err := os.RemoveAll(finalDir); err != nil {
			return Installation{}, fmt.Errorf("remove corrupt model installation: %w", err)
		}
	}

	entries, err := m.listTree(ctx, descriptor, revision)
	if err != nil {
		return Installation{}, err
	}
	artifacts, err := selectArtifacts(m.endpoint, descriptor, revision, entries)
	if err != nil {
		return Installation{}, err
	}
	stageDir := filepath.Join(m.root, descriptor.Alias, ".staging-"+revision)
	if err := os.MkdirAll(stageDir, 0o700); err != nil {
		return Installation{}, fmt.Errorf("create model staging directory: %w", err)
	}
	if err := rejectSymlinkChain(stageDir, m.root); err != nil {
		return Installation{}, err
	}
	manifestFiles := make([]ArtifactManifest, 0, len(artifacts))
	for _, item := range artifacts {
		if err := ctx.Err(); err != nil {
			return Installation{}, err
		}
		path, err := safeJoin(stageDir, item.Path)
		if err != nil {
			return Installation{}, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return Installation{}, fmt.Errorf("create artifact directory: %w", err)
		}
		if err := rejectSymlinkChain(path, m.root); err != nil {
			return Installation{}, err
		}
		if err := m.downloadArtifact(ctx, descriptor.Alias, item, path, opts.Progress); err != nil {
			return Installation{}, err
		}
		sum, size, err := fileDigest(path)
		if err != nil {
			return Installation{}, fmt.Errorf("hash artifact %s: %w", item.Path, err)
		}
		manifestFiles = append(manifestFiles, ArtifactManifest{Path: item.Path, Size: size, SHA256: sum})
	}
	manifest := ModelManifest{
		SchemaVersion: modelSchema,
		Engine:        "laya",
		Model:         descriptor.Alias,
		Source:        ModelSource{Type: "huggingface", Repository: descriptor.Repository, Revision: revision},
		Runtime:       RuntimeArtifact{Format: descriptor.runtimeFormat(), Note: descriptor.runtimeNote()},
		Files:         manifestFiles,
		InstalledAt:   time.Now().UTC().Format(time.RFC3339),
	}
	if err := writeManifest(filepath.Join(stageDir, modelManifestName), manifest); err != nil {
		return Installation{}, err
	}
	if err := syncDir(stageDir); err != nil {
		return Installation{}, err
	}
	if err := os.Rename(stageDir, finalDir); err != nil {
		if existing, inspectErr := m.inspectPath(finalDir, descriptor, true); inspectErr == nil && existing.Valid {
			return existing, nil
		}
		return Installation{}, fmt.Errorf("atomically install model: %w", err)
	}
	if opts.Progress != nil {
		opts.Progress(Progress{Model: modelID(descriptor.Alias), Done: true})
	}
	return Installation{ID: modelID(descriptor.Alias), Path: finalDir, Manifest: manifest, Valid: true}, nil
}

func (m *ModelManager) resolveRevision(ctx context.Context, descriptor ModelDescriptor, requested string) (string, error) {
	if requested == "" {
		requested = "main"
	}
	if safeRevision(requested) && isHexRevision(requested) {
		return requested, nil
	}
	endpoint := m.apiURL("models/"+descriptor.Repository, url.Values{"revision": []string{requested}})
	var info hubModelInfo
	if err := m.getJSON(ctx, endpoint, &info); err != nil {
		return "", fmt.Errorf("resolve %s revision %q: %w", descriptor.Repository, requested, err)
	}
	if !isHexRevision(info.SHA) {
		return "", fmt.Errorf("hugging face returned invalid revision %q", info.SHA)
	}
	return info.SHA, nil
}

func (m *ModelManager) listTree(ctx context.Context, descriptor ModelDescriptor, revision string) ([]hubTreeEntry, error) {
	path := "models/" + descriptor.Repository + "/tree/" + url.PathEscape(revision)
	query := url.Values{"recursive": []string{"true"}, "expand": []string{"true"}, "limit": []string{"1000"}}
	var entries []hubTreeEntry
	if err := m.getJSON(ctx, m.apiURL(path, query), &entries); err != nil {
		return nil, fmt.Errorf("list %s artifacts: %w", descriptor.Repository, err)
	}
	return entries, nil
}

func selectArtifacts(endpoint *url.URL, descriptor ModelDescriptor, revision string, entries []hubTreeEntry) ([]artifact, error) {
	prefix := strings.Trim(descriptor.Subdirectory, "/")
	if prefix != "" {
		prefix += "/"
	}
	selected := make([]artifact, 0)
	for _, entry := range entries {
		if entry.Type != "file" || !safeRelativePath(entry.Path) {
			continue
		}
		if !strings.HasPrefix(entry.Path, prefix) {
			continue
		}
		rel := strings.TrimPrefix(entry.Path, prefix)
		if rel != "model.safetensors" && rel != "rl_agent_config.json" && rel != "mlx_config.json" && !strings.HasPrefix(rel, "tokenizer/") && !strings.HasPrefix(rel, "encoder/") {
			continue
		}
		if entry.Size < 0 || entry.Size > maxModelFileBytes {
			return nil, fmt.Errorf("artifact %q has unsupported size %d", entry.Path, entry.Size)
		}
		sum := ""
		if entry.LFS != nil {
			sum = strings.TrimPrefix(strings.ToLower(entry.LFS.SHA256), "sha256:")
		}
		artifactURL := strings.TrimRight(endpoint.String(), "/") + "/" + escapedPath(descriptor.Repository) + "/resolve/" + url.PathEscape(revision) + "/" + escapedPath(entry.Path)
		selected = append(selected, artifact{Path: rel, Size: entry.Size, SHA256: sum, URL: artifactURL})
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Path < selected[j].Path })
	hasModel, hasConfig, hasTokenizer, hasEncoder, hasMLXConfig := false, false, false, false, false
	for _, item := range selected {
		switch {
		case item.Path == "model.safetensors":
			hasModel = true
		case item.Path == "rl_agent_config.json":
			hasConfig = true
		case item.Path == "mlx_config.json":
			hasMLXConfig = true
		case strings.HasPrefix(item.Path, "tokenizer/"):
			hasTokenizer = true
		case strings.HasPrefix(item.Path, "encoder/"):
			hasEncoder = true
		}
	}
	if !hasModel || !hasConfig || !hasTokenizer || !hasEncoder || (descriptor.runtimeFormat() == RuntimeFormatMLX && !hasMLXConfig) {
		return nil, fmt.Errorf("laya checkpoint %q is incomplete (model=%v config=%v tokenizer=%v encoder=%v mlx_config=%v)", descriptor.Alias, hasModel, hasConfig, hasTokenizer, hasEncoder, hasMLXConfig)
	}
	return selected, nil
}

func (m *ModelManager) downloadArtifact(ctx context.Context, model string, item artifact, final string, progress func(Progress)) error {
	partial := final + ".partial"
	var offset int64
	if info, err := os.Stat(partial); err == nil {
		offset = info.Size()
		if item.Size > 0 && offset > item.Size {
			if err := os.Remove(partial); err != nil {
				return fmt.Errorf("discard oversized partial %s: %w", item.Path, err)
			}
			offset = 0
		}
	}
	if item.Size > 0 && offset == item.Size {
		if err := verifyFile(partial, item.Size, item.SHA256); err == nil {
			return os.Rename(partial, final)
		}
		offset = 0
		if err := os.Remove(partial); err != nil {
			return fmt.Errorf("remove invalid partial %s: %w", item.Path, err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, item.URL, nil)
	if err != nil {
		return fmt.Errorf("create artifact request: %w", err)
	}
	if m.token != "" {
		req.Header.Set("Authorization", "Bearer "+m.token)
	}
	if offset > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-")
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("download artifact %s: %w", item.Path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("download artifact %s: HTTP %s", item.Path, resp.Status)
	}
	if offset > 0 && resp.StatusCode == http.StatusOK {
		offset = 0
	}
	flags := os.O_CREATE | os.O_WRONLY
	if offset == 0 {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_APPEND
	}
	f, err := os.OpenFile(partial, flags, 0o600)
	if err != nil {
		return fmt.Errorf("open partial artifact %s: %w", item.Path, err)
	}
	limit := maxModelFileBytes
	if item.Size > 0 {
		limit = item.Size - offset
	}
	n, copyErr := io.Copy(f, io.LimitReader(resp.Body, limit+1))
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil {
		return fmt.Errorf("write artifact %s: %w", item.Path, firstError(copyErr, closeErr))
	}
	total := offset + n
	if item.Size > 0 && total > item.Size {
		return fmt.Errorf("artifact %s exceeded expected size %d", item.Path, item.Size)
	}
	if progress != nil {
		progress(Progress{Model: modelID(model), Artifact: item.Path, Downloaded: total, Total: item.Size})
	}
	if item.Size > 0 && total != item.Size {
		return fmt.Errorf("artifact %s incomplete: got %d bytes, want %d", item.Path, total, item.Size)
	}
	if err := verifyFile(partial, item.Size, item.SHA256); err != nil {
		return fmt.Errorf("verify artifact %s: %w", item.Path, err)
	}
	if err := os.Rename(partial, final); err != nil {
		return fmt.Errorf("publish artifact %s: %w", item.Path, err)
	}
	return nil
}

func (m *ModelManager) getJSON(ctx context.Context, endpoint string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	if m.token != "" {
		req.Header.Set("Authorization", "Bearer "+m.token)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 2<<20))
	return decoder.Decode(target)
}

func (m *ModelManager) apiURL(path string, query url.Values) string {
	u := *m.endpoint
	u.Path = strings.TrimRight(m.endpoint.Path, "/") + "/api/" + strings.TrimLeft(path, "/")
	u.RawQuery = query.Encode()
	return u.String()
}

// ListInstalled reads manifests without hashing large model files.
func (m *ModelManager) ListInstalled() ([]Installation, error) {
	var result []Installation
	for _, descriptor := range m.catalog {
		dir := filepath.Join(m.root, descriptor.Alias)
		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("list installed %s: %w", descriptor.Alias, err)
		}
		for _, entry := range entries {
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			installation, inspectErr := m.inspectPath(filepath.Join(dir, entry.Name()), descriptor, false)
			if inspectErr == nil {
				result = append(result, installation)
			}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].ID+result[i].Manifest.Source.Revision < result[j].ID+result[j].Manifest.Source.Revision
	})
	return result, nil
}

func (m *ModelManager) Inspect(id string) ([]Installation, error) {
	descriptor, err := m.descriptor(id)
	if err != nil {
		return nil, err
	}
	all, err := m.ListInstalled()
	if err != nil {
		return nil, err
	}
	filtered := all[:0]
	for _, installation := range all {
		if installation.Manifest.Model == descriptor.Alias {
			filtered = append(filtered, installation)
		}
	}
	return filtered, nil
}

// Verify hashes every installed artifact for the selected model. It returns
// invalid installations as records with Valid=false so CLI callers can report
// corruption without losing the manifest's source identity.
func (m *ModelManager) Verify(id string) ([]Installation, error) {
	descriptor, err := m.descriptor(id)
	if err != nil {
		return nil, err
	}
	all, err := m.ListInstalled()
	if err != nil {
		return nil, err
	}
	result := make([]Installation, 0, len(all))
	for _, installation := range all {
		if installation.Manifest.Model != descriptor.Alias {
			continue
		}
		checked, checkErr := m.inspectPath(installation.Path, descriptor, true)
		if checkErr != nil {
			installation.Valid = false
			result = append(result, installation)
			continue
		}
		result = append(result, checked)
	}
	return result, nil
}

// Remove deletes one explicitly selected revision. It never accepts an empty
// revision, so a caller cannot accidentally remove every installed model.
func (m *ModelManager) Remove(id, revision string) error {
	descriptor, err := m.descriptor(id)
	if err != nil {
		return err
	}
	if !safeRevision(revision) {
		return fmt.Errorf("invalid model revision %q", revision)
	}
	path := filepath.Join(m.root, descriptor.Alias, revision)
	if _, err := m.inspectPath(path, descriptor, false); err != nil {
		return fmt.Errorf("inspect model before removal: %w", err)
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove model %s: %w", id, err)
	}
	return nil
}

func (m *ModelManager) inspectPath(dir string, descriptor ModelDescriptor, hash bool) (Installation, error) {
	if err := rejectSymlinkChain(dir, m.root); err != nil {
		return Installation{}, err
	}
	data, err := os.ReadFile(filepath.Join(dir, modelManifestName))
	if err != nil {
		return Installation{}, err
	}
	var manifest ModelManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Installation{}, fmt.Errorf("parse model manifest: %w", err)
	}
	valid := manifest.SchemaVersion == modelSchema && manifest.Engine == "laya" && manifest.Model == descriptor.Alias && manifest.Source.Repository == descriptor.Repository && validateRuntimeArtifact(manifest.Runtime) == nil
	if hash && valid {
		for _, file := range manifest.Files {
			path, pathErr := safeJoin(dir, file.Path)
			if pathErr != nil || rejectSymlinkChain(path, m.root) != nil || verifyFile(path, file.Size, file.SHA256) != nil {
				valid = false
				break
			}
		}
		if valid && manifest.Runtime.Ready {
			path, pathErr := safeJoin(dir, manifest.Runtime.Path)
			if pathErr != nil || rejectSymlinkChain(path, m.root) != nil || verifyFile(path, manifest.Runtime.Size, manifest.Runtime.SHA256) != nil {
				valid = false
			}
		}
	}
	return Installation{ID: modelID(descriptor.Alias), Path: dir, Manifest: manifest, Valid: valid}, nil
}

func writeManifest(path string, manifest ModelManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal model manifest: %w", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".manifest-*.tmp")
	if err != nil {
		return fmt.Errorf("create model manifest: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write model manifest: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync model manifest: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("publish model manifest: %w", err)
	}
	return nil
}

func verifyFile(path string, size int64, expected string) error {
	actual, gotSize, err := fileDigest(path)
	if err != nil {
		return err
	}
	if size >= 0 && gotSize != size {
		return fmt.Errorf("size %d, want %d", gotSize, size)
	}
	if expected != "" && !strings.EqualFold(actual, expected) {
		return fmt.Errorf("sha256 %s, want %s", actual, expected)
	}
	return nil
}

func fileDigest(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxModelFileBytes+1))
	if err != nil {
		return "", n, err
	}
	if n > maxModelFileBytes {
		return "", n, fmt.Errorf("file exceeds %d-byte limit", maxModelFileBytes)
	}
	if info.Size() != n {
		return "", n, fmt.Errorf("file changed while hashing")
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func safeJoin(root, relative string) (string, error) {
	if !safeRelativePath(relative) {
		return "", fmt.Errorf("unsafe model artifact path %q", relative)
	}
	path := filepath.Join(root, filepath.FromSlash(relative))
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	cleanPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if cleanPath != cleanRoot && !strings.HasPrefix(cleanPath, cleanRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("model artifact escapes store: %q", relative)
	}
	return cleanPath, nil
}

func rejectSymlinkChain(path, root string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve model root: %w", err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve model path: %w", err)
	}
	if path != root && !strings.HasPrefix(path, root+string(filepath.Separator)) {
		return fmt.Errorf("model path escapes store: %q", path)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	current := root
	if rel == "." {
		return nil
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if statErr != nil {
			if os.IsNotExist(statErr) {
				continue
			}
			return fmt.Errorf("inspect model path %q: %w", current, statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink in model store: %q", current)
		}
	}
	return nil
}

func safeRelativePath(path string) bool {
	if path == "" || strings.HasPrefix(path, "/") || strings.ContainsRune(path, '\x00') {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	return clean == path && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}

func escapedPath(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func safeRevision(revision string) bool {
	return revision != "" && len(revision) <= 128 && !strings.ContainsAny(revision, `/\\`) && !strings.ContainsAny(revision, "\x00 \t\r\n") && revision != "." && revision != ".."
}

func isHexRevision(revision string) bool {
	if len(revision) < 7 || len(revision) > 64 {
		return false
	}
	for _, r := range revision {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || strings.HasPrefix(host, "127.")
}

func allowedModelRedirectHost(host, configured string) bool {
	host = strings.ToLower(host)
	configured = strings.ToLower(configured)
	if host == configured {
		return true
	}
	// These are the documented Hugging Face object-storage host families used
	// by resolve URLs. Keep the suffixes narrow; arbitrary redirects remain
	// rejected.
	return strings.HasSuffix(host, ".huggingface.co") || strings.HasSuffix(host, ".hf.co")
}

func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open model directory for sync: %w", err)
	}
	defer f.Close()
	if err := f.Sync(); err != nil && runtime.GOOS != "windows" {
		return fmt.Errorf("sync model directory: %w", err)
	}
	return nil
}

func firstError(a, b error) error {
	if a != nil {
		return a
	}
	return b
}
