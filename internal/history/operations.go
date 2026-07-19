package history

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/patrikcze/llmtui/internal/tools"
)

const operationLogVersion = 1

// OperationState describes what a durable journal already knows about one
// idempotency key.
type OperationState int

const (
	OperationNew OperationState = iota
	OperationStarted
	OperationCompleted
)

// OperationDecision is returned before a side effect. Started means a prior
// process may have performed the effect before crashing; Completed means it
// definitely recorded an outcome. Neither state is safe to execute again.
type OperationDecision struct {
	State     OperationState
	Succeeded bool
}

type operationRecord struct {
	Version   int       `json:"version"`
	Time      time.Time `json:"time"`
	Key       string    `json:"key"`
	CallID    string    `json:"call_id,omitempty"`
	Tool      string    `json:"tool"`
	Phase     string    `json:"phase"`
	Succeeded bool      `json:"succeeded,omitempty"`
}

// OperationLog is an append-only, fsynced per-session write-ahead log. It
// stores hashes and tool names, never command bodies, paths, arguments, or
// outputs.
type OperationLog struct {
	mu     sync.Mutex
	path   string
	states map[string]operationRecord
}

// OpenOperationLog opens (or recovers) the journal for one session.
func OpenOperationLog(dir, session string) (*OperationLog, error) {
	if err := validName(session); err != nil {
		return nil, err
	}
	operationsDir := filepath.Join(dir, ".operations")
	if err := os.MkdirAll(operationsDir, 0o700); err != nil {
		return nil, fmt.Errorf("create operation log directory: %w", err)
	}
	path := filepath.Join(operationsDir, session+".jsonl")
	log := &OperationLog{path: path, states: make(map[string]operationRecord)}
	if err := log.recover(); err != nil {
		return nil, err
	}
	return log, nil
}

// IsDurableSideEffect reports calls that must be journaled before execution.
// MCP tools are treated as mutating because their schemas do not communicate
// effect semantics.
func IsDurableSideEffect(c tools.Call) bool {
	return c.MCPServer != "" || c.Tool == tools.ToolWriteFile || c.Tool == tools.ToolRunCommand
}

// Begin durably records intent before a side effect. Existing started or
// completed keys are returned without appending and must not be re-executed.
func (l *OperationLog) Begin(c tools.Call) (OperationDecision, error) {
	if l == nil {
		return OperationDecision{}, errors.New("operation log is unavailable")
	}
	key := operationKey(c)
	l.mu.Lock()
	defer l.mu.Unlock()
	if record, ok := l.states[key]; ok {
		if record.Phase == "completed" {
			return OperationDecision{State: OperationCompleted, Succeeded: record.Succeeded}, nil
		}
		return OperationDecision{State: OperationStarted}, nil
	}
	record := operationRecord{
		Version: operationLogVersion,
		Time:    time.Now().UTC(),
		Key:     key,
		CallID:  c.ID,
		Tool:    operationTool(c),
		Phase:   "started",
	}
	if err := l.append(record); err != nil {
		return OperationDecision{}, err
	}
	l.states[key] = record
	return OperationDecision{State: OperationNew}, nil
}

// Complete durably records the observed outcome after execution.
func (l *OperationLog) Complete(c tools.Call, succeeded bool) error {
	if l == nil {
		return errors.New("operation log is unavailable")
	}
	key := operationKey(c)
	l.mu.Lock()
	defer l.mu.Unlock()
	record, ok := l.states[key]
	if !ok || record.Phase != "started" {
		return fmt.Errorf("operation %s was not started", key)
	}
	record.Time = time.Now().UTC()
	record.Phase = "completed"
	record.Succeeded = succeeded
	if err := l.append(record); err != nil {
		return err
	}
	l.states[key] = record
	return nil
}

func (l *OperationLog) recover() error {
	file, err := os.Open(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open operation log: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		var record operationRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return fmt.Errorf("parse operation log line %d: %w", line, err)
		}
		if record.Version != operationLogVersion || record.Key == "" ||
			(record.Phase != "started" && record.Phase != "completed") {
			return fmt.Errorf("invalid operation log record on line %d", line)
		}
		l.states[record.Key] = record
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read operation log: %w", err)
	}
	return nil
}

func (l *OperationLog) append(record operationRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode operation record: %w", err)
	}
	data = append(data, '\n')
	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open operation log: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return fmt.Errorf("secure operation log: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return fmt.Errorf("append operation log: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("sync operation log: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close operation log: %w", err)
	}
	return nil
}

func operationKey(c tools.Call) string {
	var material string
	if strings.TrimSpace(c.ID) != "" {
		material = "call-id\x00" + c.ID + "\x00" + operationTool(c)
	} else {
		// Fenced calls have no provider ID. Their content hash becomes the
		// idempotency key, favoring duplicate prevention over silently
		// repeating an identical non-idempotent command after recovery.
		material = strings.Join([]string{
			"content", operationTool(c), c.Path, c.Body, c.MCPArgs,
		}, "\x00")
	}
	sum := sha256.Sum256([]byte(material))
	return hex.EncodeToString(sum[:])
}

func operationTool(c tools.Call) string {
	if c.MCPServer != "" {
		return "mcp:" + c.MCPServer + "/" + c.MCPTool
	}
	return c.Tool
}
