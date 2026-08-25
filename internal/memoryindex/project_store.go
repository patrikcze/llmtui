package memoryindex

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	projectStoreVersion  = 1
	maxProjectRecords    = 256
	maxProjectRecordText = 16 * 1024
	maxProjectStoreBytes = 1024 * 1024
)

var (
	// ErrProjectRecordNotFound means no project record matched an ID or
	// unambiguous ID prefix.
	ErrProjectRecordNotFound = errors.New("memoryindex: project record not found")
	// ErrCorruptProjectStore means a project file is malformed, unsupported,
	// oversized, or belongs to a different workspace.
	ErrCorruptProjectStore = errors.New("memoryindex: corrupt project store")
	// ErrProjectStoreFull means adding another record would exceed the
	// bounded project-memory record count.
	ErrProjectStoreFull = errors.New("memoryindex: project store full")
)

var (
	projectSecretAssignmentPattern = regexp.MustCompile(
		`(?i)((?:token|secret|password|passwd|authorization|api[_-]?key)\s*[=:]\s*)[^\s,;}]+`,
	)
	projectBearerPattern     = regexp.MustCompile(`(?i)\bbearer\s+[a-z0-9._~+/=-]{8,}`)
	projectKeyPattern        = regexp.MustCompile(`\b(?:sk|ghp|github_pat)-[A-Za-z0-9_-]{8,}\b`)
	projectPrivateKeyPattern = regexp.MustCompile(
		`(?s)-----BEGIN [^-\n]*PRIVATE KEY-----.*?-----END [^-\n]*PRIVATE KEY-----`,
	)
)

// ReviewState controls whether a project record participates in normal
// retrieval. The zero value is invalid in persisted records.
type ReviewState string

const (
	ReviewUnknown  ReviewState = ""
	ReviewApproved ReviewState = "approved"
	ReviewProposed ReviewState = "proposed"
)

// Project identifies one canonical workspace without persisting or exposing
// its path as the storage key.
type Project struct {
	ID   string
	Root string
}

// ResolveProject canonicalizes root, resolves symlinks, and hashes the result
// into the stable workspace ID used for project-memory isolation.
func ResolveProject(root string) (Project, error) {
	if strings.TrimSpace(root) == "" {
		return Project{}, fmt.Errorf("resolve project: workspace root is empty")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return Project{}, fmt.Errorf("resolve project path: %w", err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return Project{}, fmt.Errorf("resolve project symlinks: %w", err)
	}
	canonicalRoot := filepath.Clean(resolvedRoot)
	sum := sha256.Sum256([]byte(canonicalRoot))
	return Project{
		ID:   hex.EncodeToString(sum[:]),
		Root: canonicalRoot,
	}, nil
}

// ProjectRecord is one typed, workspace-scoped durable memory entry.
type ProjectRecord struct {
	ID          string      `json:"id"`
	Text        string      `json:"text"`
	Kind        Kind        `json:"kind"`
	Scope       Scope       `json:"scope"`
	ProjectID   string      `json:"project_id"`
	Tags        []string    `json:"tags"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
	Trust       TrustClass  `json:"trust"`
	Review      ReviewState `json:"review"`
	SourceRunID string      `json:"source_run_id,omitempty"`
	SourceCycle int         `json:"source_cycle,omitempty"`
	ContentHash string      `json:"content_hash"`
}

type projectFile struct {
	Version   int             `json:"version"`
	ProjectID string          `json:"project_id"`
	Records   []ProjectRecord `json:"records"`
}

// ProjectStore atomically persists bounded records for exactly one workspace.
type ProjectStore struct {
	dir       string
	projectID string
	mu        sync.Mutex
}

// NewProjectStore constructs a workspace-isolated store below dir.
func NewProjectStore(dir, projectID string) (*ProjectStore, error) {
	if !validProjectID(projectID) {
		return nil, fmt.Errorf("new project store: invalid project id")
	}
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("new project store: directory is empty")
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("new project store path: %w", err)
	}
	return &ProjectStore{
		dir:       filepath.Clean(absDir),
		projectID: projectID,
	}, nil
}

// ProjectID returns the workspace identity this store is confined to.
func (s *ProjectStore) ProjectID() string {
	return s.projectID
}

// Path returns the private JSON record path used by this store.
func (s *ProjectStore) Path() string {
	return filepath.Join(s.dir, s.projectID+".json")
}

// Load returns all validated records. A missing file is an empty store.
func (s *ProjectStore) Load() ([]ProjectRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

// Find returns one record by exact ID or unambiguous ID prefix.
func (s *ProjectStore) Find(id string) (ProjectRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.loadLocked()
	if err != nil {
		return ProjectRecord{}, err
	}
	index, err := findProjectRecord(records, id)
	if err != nil {
		return ProjectRecord{}, err
	}
	return records[index], nil
}

// Add stores an approved, user-authored project record.
func (s *ProjectStore) Add(kind Kind, text string, tags ...string) (ProjectRecord, error) {
	return s.add(kind, text, TrustUserAuthored, ReviewApproved, "", 0, tags)
}

// Propose stores an inactive model-proposed record. It remains excluded from
// normal retrieval until Approve records explicit user review.
func (s *ProjectStore) Propose(kind Kind, text string, tags ...string) (ProjectRecord, error) {
	return s.add(kind, text, TrustModelProposed, ReviewProposed, "", 0, tags)
}

// Promote atomically stores a user-approved outcome from a verifier-passed
// agent run. Model-proposed trust is preserved even after explicit approval.
func (s *ProjectStore) Promote(
	kind Kind,
	text string,
	runID string,
	cycle int,
	tags ...string,
) (ProjectRecord, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" || cycle <= 0 {
		return ProjectRecord{}, fmt.Errorf("promote project record: run id and positive cycle are required")
	}
	return s.add(kind, text, TrustModelProposed, ReviewApproved, runID, cycle, tags)
}

func (s *ProjectStore) add(
	kind Kind,
	text string,
	trust TrustClass,
	review ReviewState,
	sourceRunID string,
	sourceCycle int,
	tags []string,
) (ProjectRecord, error) {
	text = redactProjectSecrets(strings.TrimSpace(text))
	if text == "" {
		return ProjectRecord{}, fmt.Errorf("add project record: text is empty")
	}
	if len([]byte(text)) > maxProjectRecordText {
		return ProjectRecord{}, fmt.Errorf("add project record: text exceeds %d bytes", maxProjectRecordText)
	}
	if !isProjectKind(kind) {
		return ProjectRecord{}, fmt.Errorf("add project record: unsupported kind %q", kind)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.loadLocked()
	if err != nil {
		return ProjectRecord{}, err
	}
	if len(records) >= maxProjectRecords {
		return ProjectRecord{}, ErrProjectStoreFull
	}

	id, err := newProjectRecordID(records)
	if err != nil {
		return ProjectRecord{}, fmt.Errorf("add project record id: %w", err)
	}
	now := time.Now().UTC()
	record := ProjectRecord{
		ID:          id,
		Text:        text,
		Kind:        kind,
		Scope:       ScopeProject,
		ProjectID:   s.projectID,
		Tags:        cleanProjectTags(tags),
		CreatedAt:   now,
		UpdatedAt:   now,
		Trust:       trust,
		Review:      review,
		SourceRunID: sourceRunID,
		SourceCycle: sourceCycle,
		ContentHash: contentHash(text),
	}
	records = append(records, record)
	if err := s.saveLocked(records); err != nil {
		return ProjectRecord{}, err
	}
	return record, nil
}

// Approve makes a pending proposal eligible for retrieval while preserving
// its model-proposed provenance.
func (s *ProjectStore) Approve(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.loadLocked()
	if err != nil {
		return err
	}
	index, err := findProjectRecord(records, id)
	if err != nil {
		return err
	}
	if records[index].Review == ReviewApproved {
		return nil
	}
	if records[index].Review != ReviewProposed {
		return fmt.Errorf("approve project record: invalid review state %q", records[index].Review)
	}
	records[index].Review = ReviewApproved
	records[index].UpdatedAt = time.Now().UTC()
	return s.saveLocked(records)
}

// Remove deletes one record by exact ID or unambiguous ID prefix.
func (s *ProjectStore) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.loadLocked()
	if err != nil {
		return err
	}
	index, err := findProjectRecord(records, id)
	if err != nil {
		return err
	}
	records = slices.Delete(records, index, index+1)
	return s.saveLocked(records)
}

func (s *ProjectStore) loadLocked() ([]ProjectRecord, error) {
	data, err := readProjectFile(s.Path())
	if errors.Is(err, os.ErrNotExist) {
		return []ProjectRecord{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read project store: %w", err)
	}
	var stored projectFile
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("%w: decode json", ErrCorruptProjectStore)
	}
	if stored.Version != projectStoreVersion {
		return nil, fmt.Errorf("%w: unsupported version %d", ErrCorruptProjectStore, stored.Version)
	}
	if stored.ProjectID != s.projectID {
		return nil, fmt.Errorf("%w: workspace identity mismatch", ErrCorruptProjectStore)
	}
	if len(stored.Records) > maxProjectRecords {
		return nil, fmt.Errorf("%w: too many records", ErrCorruptProjectStore)
	}
	seenIDs := make(map[string]struct{}, len(stored.Records))
	for i := range stored.Records {
		if err := validateProjectRecord(stored.Records[i], s.projectID); err != nil {
			return nil, fmt.Errorf("%w: record %d: %v", ErrCorruptProjectStore, i, err)
		}
		if _, exists := seenIDs[stored.Records[i].ID]; exists {
			return nil, fmt.Errorf("%w: duplicate record id", ErrCorruptProjectStore)
		}
		seenIDs[stored.Records[i].ID] = struct{}{}
	}
	return stored.Records, nil
}

func (s *ProjectStore) saveLocked(records []ProjectRecord) error {
	stored := projectFile{
		Version:   projectStoreVersion,
		ProjectID: s.projectID,
		Records:   records,
	}
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return fmt.Errorf("encode project store: %w", err)
	}
	if len(data) > maxProjectStoreBytes {
		return fmt.Errorf("save project store: %w: exceeds %d bytes", ErrCorruptProjectStore, maxProjectStoreBytes)
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("create project store directory: %w", err)
	}
	if err := os.Chmod(s.dir, 0o700); err != nil {
		return fmt.Errorf("secure project store directory: %w", err)
	}

	tmp, err := os.CreateTemp(s.dir, ".project-*.tmp")
	if err != nil {
		return fmt.Errorf("create project store temp file: %w", err)
	}
	tmpPath := tmp.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secure project store temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write project store: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync project store: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close project store: %w", err)
	}
	if err := replaceProjectFile(tmpPath, s.Path()); err != nil {
		return fmt.Errorf("replace project store: %w", err)
	}
	removeTemp = false
	return nil
}

func readProjectFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > maxProjectStoreBytes {
		return nil, fmt.Errorf("%w: file exceeds %d bytes", ErrCorruptProjectStore, maxProjectStoreBytes)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxProjectStoreBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxProjectStoreBytes {
		return nil, fmt.Errorf("%w: file exceeds %d bytes", ErrCorruptProjectStore, maxProjectStoreBytes)
	}
	return data, nil
}

func validateProjectRecord(record ProjectRecord, projectID string) error {
	if !validProjectRecordID(record.ID) {
		return fmt.Errorf("invalid record id")
	}
	if strings.TrimSpace(record.Text) == "" || len([]byte(record.Text)) > maxProjectRecordText {
		return fmt.Errorf("invalid record text")
	}
	if !isProjectKind(record.Kind) || record.Scope != ScopeProject || record.ProjectID != projectID {
		return fmt.Errorf("invalid project scope")
	}
	if record.CreatedAt.IsZero() || record.UpdatedAt.IsZero() || record.UpdatedAt.Before(record.CreatedAt) {
		return fmt.Errorf("invalid record timestamps")
	}
	if record.ContentHash != contentHash(record.Text) {
		return fmt.Errorf("invalid content hash")
	}
	validUserRecord := record.Trust == TrustUserAuthored && record.Review == ReviewApproved
	validProposal := record.Trust == TrustModelProposed &&
		(record.Review == ReviewProposed || record.Review == ReviewApproved)
	if !validUserRecord && !validProposal {
		return fmt.Errorf("invalid trust or review state")
	}
	return nil
}

func isProjectKind(kind Kind) bool {
	switch kind {
	case KindProjectArchitecture, KindProjectConvention, KindProjectDecision:
		return true
	default:
		return false
	}
}

func validProjectID(id string) bool {
	if len(id) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func validProjectRecordID(id string) bool {
	const prefix = "project-"
	if !strings.HasPrefix(id, prefix) || len(id) != len(prefix)+12 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(id, prefix))
	return err == nil
}

func newProjectRecordID(records []ProjectRecord) (string, error) {
	existing := make(map[string]struct{}, len(records))
	for _, record := range records {
		existing[record.ID] = struct{}{}
	}
	for range 8 {
		random := make([]byte, 6)
		if _, err := rand.Read(random); err != nil {
			return "", err
		}
		id := "project-" + hex.EncodeToString(random)
		if _, exists := existing[id]; !exists {
			return id, nil
		}
	}
	return "", fmt.Errorf("could not allocate unique record id")
}

func findProjectRecord(records []ProjectRecord, id string) (int, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return -1, ErrProjectRecordNotFound
	}
	for i, record := range records {
		if record.ID == id {
			return i, nil
		}
	}
	if len(id) < 4 {
		return -1, ErrProjectRecordNotFound
	}
	matched := -1
	for i, record := range records {
		if !strings.HasPrefix(record.ID, id) {
			continue
		}
		if matched >= 0 {
			return -1, fmt.Errorf("project record id prefix %q is ambiguous", id)
		}
		matched = i
	}
	if matched < 0 {
		return -1, ErrProjectRecordNotFound
	}
	return matched, nil
}

func cleanProjectTags(tags []string) []string {
	cleaned := make([]string, 0, min(len(tags), 32))
	seen := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" || len([]byte(tag)) > 128 {
			continue
		}
		if _, exists := seen[tag]; exists {
			continue
		}
		seen[tag] = struct{}{}
		cleaned = append(cleaned, tag)
		if len(cleaned) == 32 {
			break
		}
	}
	return cleaned
}

func redactProjectSecrets(value string) string {
	value = projectPrivateKeyPattern.ReplaceAllString(value, "[REDACTED PRIVATE KEY]")
	value = projectBearerPattern.ReplaceAllString(value, "Bearer [REDACTED]")
	value = projectSecretAssignmentPattern.ReplaceAllString(value, `${1}[REDACTED]`)
	return projectKeyPattern.ReplaceAllString(value, "[REDACTED KEY]")
}
