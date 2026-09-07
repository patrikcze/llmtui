package personalapps

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const mutationLedgerVersion = 1

// MutationLedger is a user-level, append-only write-ahead log for personal
// app mutations. It intentionally lives outside workspace and session
// history: reopening a workspace or receiving a new model call must not make
// an uncertain effect safe to repeat.
//
// Records contain only a semantic-effect digest and outcome category. The
// digest is calculated from native resource identity and intended state, but
// neither addresses, message bodies, calendar text, handles nor raw arguments
// are ever written to disk.
type MutationLedger struct {
	dir string
}

// NewMutationLedger constructs a lazy user-level ledger. It does no I/O; the
// directory is created only when an approved mutation reaches Begin.
func NewMutationLedger(dir string) *MutationLedger {
	return &MutationLedger{dir: filepath.Clean(dir)}
}

type mutationRecord struct {
	Version int           `json:"version"`
	Time    time.Time     `json:"time"`
	Key     string        `json:"key"`
	Phase   MutationState `json:"phase"`
}

// Begin persists intent before the external side effect. It serializes
// competing llmtui processes with an advisory lock and always rebuilds state
// from disk while holding that lock, so a process never makes a decision from
// a stale in-memory snapshot.
func (l *MutationLedger) Begin(ctx context.Context, change ResolvedChange) (MutationDecision, error) {
	key, err := mutationKey(change)
	if err != nil {
		return MutationDecision{}, err
	}
	return l.withRecords(ctx, func(records map[string]mutationRecord) (MutationDecision, error) {
		if record, ok := records[key]; ok {
			return MutationDecision{State: record.Phase}, nil
		}
		record := mutationRecord{
			Version: mutationLedgerVersion,
			Time:    time.Now().UTC(),
			Key:     key,
			Phase:   MutationIntentRecorded,
		}
		if err := l.append(record); err != nil {
			return MutationDecision{}, err
		}
		records[key] = record
		return MutationDecision{State: MutationNew}, nil
	})
}

// Complete records readback-derived results. A failed completion deliberately
// leaves intent_recorded on disk, which blocks an automatic retry after a
// crash or storage failure.
func (l *MutationLedger) Complete(ctx context.Context, change ResolvedChange, outcomes []ItemOutcome) error {
	key, err := mutationKey(change)
	if err != nil {
		return err
	}
	_, err = l.withRecords(ctx, func(records map[string]mutationRecord) (MutationDecision, error) {
		record, ok := records[key]
		if !ok || record.Phase != MutationIntentRecorded {
			return MutationDecision{}, fmt.Errorf("mutation %s was not recorded as intent", key)
		}
		record = mutationRecord{
			Version: mutationLedgerVersion,
			Time:    time.Now().UTC(),
			Key:     key,
			Phase:   mutationOutcomeState(outcomes),
		}
		if err := l.append(record); err != nil {
			return MutationDecision{}, err
		}
		records[key] = record
		return MutationDecision{State: record.Phase}, nil
	})
	return err
}

func mutationOutcomeState(outcomes []ItemOutcome) MutationState {
	for _, outcome := range outcomes {
		if outcome.Outcome == OutcomeUnknown {
			return MutationOutcomeUnknown
		}
	}
	for _, outcome := range outcomes {
		if outcome.Outcome == OutcomeApplied {
			return MutationVerifiedApplied
		}
	}
	return MutationVerifiedNotApplied
}

func (l *MutationLedger) withRecords(ctx context.Context, fn func(map[string]mutationRecord) (MutationDecision, error)) (MutationDecision, error) {
	if l == nil || l.dir == "." || l.dir == "" {
		return MutationDecision{}, errors.New("mutation ledger path is unavailable")
	}
	if err := os.MkdirAll(l.dir, 0o700); err != nil {
		return MutationDecision{}, fmt.Errorf("create mutation ledger directory: %w", err)
	}
	lock, err := lockMutationLedger(ctx, filepath.Join(l.dir, ".mutation-ledger.lock"))
	if err != nil {
		return MutationDecision{}, err
	}

	records, err := l.recover()
	if err == nil {
		decision, callErr := fn(records)
		if callErr != nil {
			err = callErr
		} else {
			if unlockErr := lock.Unlock(); unlockErr != nil {
				return MutationDecision{}, fmt.Errorf("unlock mutation ledger: %w", unlockErr)
			}
			return decision, nil
		}
	}
	if unlockErr := lock.Unlock(); unlockErr != nil {
		return MutationDecision{}, fmt.Errorf("unlock mutation ledger after failure: %w", unlockErr)
	}
	return MutationDecision{}, err
}

func (l *MutationLedger) recover() (map[string]mutationRecord, error) {
	file, err := os.Open(filepath.Join(l.dir, "mutations.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return make(map[string]mutationRecord), nil
	}
	if err != nil {
		return nil, fmt.Errorf("open mutation ledger: %w", err)
	}
	defer file.Close()

	records := make(map[string]mutationRecord)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4*1024), 64*1024)
	line := 0
	for scanner.Scan() {
		line++
		var record mutationRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return nil, fmt.Errorf("parse mutation ledger line %d: %w", line, err)
		}
		if record.Version != mutationLedgerVersion || record.Key == "" || !validMutationState(record.Phase) {
			return nil, fmt.Errorf("invalid mutation ledger record on line %d", line)
		}
		records[record.Key] = record
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read mutation ledger: %w", err)
	}
	return records, nil
}

func validMutationState(state MutationState) bool {
	return state == MutationIntentRecorded || state == MutationVerifiedApplied ||
		state == MutationVerifiedNotApplied || state == MutationOutcomeUnknown
}

func (l *MutationLedger) append(record mutationRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode mutation ledger record: %w", err)
	}
	data = append(data, '\n')
	file, err := os.OpenFile(filepath.Join(l.dir, "mutations.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open mutation ledger: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return fmt.Errorf("secure mutation ledger: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return fmt.Errorf("append mutation ledger: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("sync mutation ledger: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close mutation ledger: %w", err)
	}
	return nil
}

type mutationResource struct {
	Kind          HandleKind `json:"kind"`
	Adapter       Adapter    `json:"adapter"`
	AccountID     string     `json:"account_id,omitempty"`
	ContainerPath []string   `json:"container_path,omitempty"`
	NativeID      string     `json:"native_id,omitempty"`
	ExternalID    string     `json:"external_id,omitempty"`
	Occurrence    string     `json:"occurrence,omitempty"`
	Fingerprint   string     `json:"fingerprint,omitempty"`
}

func resourceIdentity(ref ResourceRef) mutationResource {
	identity := mutationResource{
		Kind:          ref.Kind,
		Adapter:       ref.Adapter,
		AccountID:     ref.AccountID,
		ContainerPath: append([]string(nil), ref.ContainerPath...),
		NativeID:      ref.NativeID,
		ExternalID:    ref.ExternalID,
		Fingerprint:   ref.Fingerprint,
	}
	if !ref.Occurrence.IsZero() {
		identity.Occurrence = ref.Occurrence.UTC().Format(time.RFC3339Nano)
	}
	return identity
}

func (r mutationResource) sortKey() string {
	data, _ := json.Marshal(r)
	return string(data)
}

func resolvedIdentity(change ResolvedChange, handle Handle) (mutationResource, error) {
	ref, ok := change.Refs[handle]
	if !ok {
		return mutationResource{}, Errorf(CodeInternal, "resolved change is missing a resource")
	}
	return resourceIdentity(ref), nil
}

// mutationKey produces a stable semantic identity. It intentionally excludes
// the random plan ID and opaque model handles. Private desired content is
// hashed before it becomes part of the outer identity; only this final digest
// reaches the journal.
func mutationKey(change ResolvedChange) (string, error) {
	var effect any
	switch c := change.Change; {
	case c.MailMove != nil:
		messages, err := mutationMessages(change, c.MailMove.Messages)
		if err != nil {
			return "", err
		}
		destination, err := resolvedIdentity(change, c.MailMove.DestinationMailboxID)
		if err != nil {
			return "", err
		}
		effect = struct {
			Messages    []mutationMessage `json:"messages"`
			Destination mutationResource  `json:"destination"`
		}{messages, destination}
	case c.MailSetRead != nil:
		messages, err := mutationMessages(change, c.MailSetRead.Messages)
		if err != nil {
			return "", err
		}
		effect = struct {
			Messages []mutationMessage `json:"messages"`
			Read     bool              `json:"read"`
		}{messages, c.MailSetRead.Read}
	case c.MailSetFlag != nil:
		messages, err := mutationMessages(change, c.MailSetFlag.Messages)
		if err != nil {
			return "", err
		}
		effect = struct {
			Messages []mutationMessage `json:"messages"`
			Flagged  bool              `json:"flagged"`
		}{messages, c.MailSetFlag.Flagged}
	case c.MailSaveDraft != nil:
		sender, err := resolvedIdentity(change, c.MailSaveDraft.SenderAccountID)
		if err != nil {
			return "", err
		}
		reply := mutationResource{}
		if c.MailSaveDraft.InReplyToMessageID != "" {
			reply, err = resolvedIdentity(change, c.MailSaveDraft.InReplyToMessageID)
			if err != nil {
				return "", err
			}
		}
		effect = struct {
			Sender  mutationResource `json:"sender"`
			Reply   mutationResource `json:"reply,omitempty"`
			Content string           `json:"content"`
		}{sender, reply, privateDigest(struct {
			To, Cc, Bcc   []string
			Subject, Body string
			IncludeQuote  bool
		}{c.MailSaveDraft.To, c.MailSaveDraft.Cc, c.MailSaveDraft.Bcc, c.MailSaveDraft.Subject, c.MailSaveDraft.Body, c.MailSaveDraft.IncludeQuote})}
	case c.CalendarCreateEvent != nil:
		calendar, err := resolvedIdentity(change, c.CalendarCreateEvent.CalendarID)
		if err != nil {
			return "", err
		}
		effect = struct {
			Calendar mutationResource `json:"calendar"`
			Content  string           `json:"content"`
		}{calendar, privateDigest(calendarCreateContent(c.CalendarCreateEvent))}
	case c.CalendarUpdateEvent != nil:
		event, err := resolvedIdentity(change, c.CalendarUpdateEvent.EventID)
		if err != nil {
			return "", err
		}
		effect = struct {
			Event           mutationResource `json:"event"`
			ExpectedVersion string           `json:"expected_version"`
			Content         string           `json:"content"`
		}{event, c.CalendarUpdateEvent.ExpectedVersion, privateDigest(calendarUpdateContent(c.CalendarUpdateEvent))}
	default:
		return "", Errorf(CodeInternal, "resolved change has no variant")
	}
	material, err := json.Marshal(struct {
		Version int        `json:"version"`
		Type    ChangeType `json:"type"`
		Effect  any        `json:"effect"`
	}{mutationLedgerVersion, change.Change.Type, effect})
	if err != nil {
		return "", fmt.Errorf("encode mutation identity: %w", err)
	}
	sum := sha256.Sum256(material)
	return hex.EncodeToString(sum[:]), nil
}

type mutationMessage struct {
	Message         mutationResource `json:"message"`
	ExpectedVersion string           `json:"expected_version"`
}

func mutationMessages(change ResolvedChange, targets []MessageTarget) ([]mutationMessage, error) {
	messages := make([]mutationMessage, 0, len(targets))
	for _, target := range targets {
		message, err := resolvedIdentity(change, target.MessageID)
		if err != nil {
			return nil, err
		}
		messages = append(messages, mutationMessage{Message: message, ExpectedVersion: target.ExpectedVersion})
	}
	sort.Slice(messages, func(i, j int) bool {
		return messages[i].Message.sortKey()+"\x00"+messages[i].ExpectedVersion < messages[j].Message.sortKey()+"\x00"+messages[j].ExpectedVersion
	})
	return messages, nil
}

func privateDigest(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		// All inputs above are fixed DTOs. Keep the fallback deterministic and
		// content-free if a future field unexpectedly cannot marshal.
		data = []byte(fmt.Sprintf("unencodable:%T", value))
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func calendarCreateContent(change *CalendarCreateEventChange) any {
	return struct {
		Title, Notes, Location, Timezone string
		Start, End                       *Timestamp
		AllDayStart, AllDayEnd           *DateOnly
	}{
		change.Title, change.Notes, change.Location, change.Timezone,
		change.Start, change.End, change.AllDayStart, change.AllDayEnd,
	}
}

func calendarUpdateContent(change *CalendarUpdateEventChange) any {
	return struct {
		Timezone               string
		Title, Notes, Location *string
		Start, End             *Timestamp
		AllDayStart, AllDayEnd *DateOnly
	}{
		change.Timezone,
		change.Title, change.Notes, change.Location,
		change.Start, change.End, change.AllDayStart, change.AllDayEnd,
	}
}

// LedgerPath returns the path that contains the append-only file. It exists
// for diagnostics and tests; callers must not use it to read private content.
func (l *MutationLedger) LedgerPath() string {
	if l == nil {
		return ""
	}
	return l.dir
}

// mutationRecordsForTest reads no personal data because records contain only
// hashes. Keeping it unexported prevents application code from treating the
// ledger as a query API.
func mutationRecordsForTest(dir string) ([]mutationRecord, error) {
	file, err := os.Open(filepath.Join(dir, "mutations.jsonl"))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var records []mutationRecord
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var record mutationRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, scanner.Err()
}
