package tools

import (
	"fmt"
	"strings"

	"github.com/patrikcze/llmtui/internal/entity"
)

const (
	MaxEntityDetailsIDs          = 8
	MaxEntityDetailsPayloadBytes = 4 * 1024
	MaxEntityQueryBytes          = 512
	MaxEntityDetailsKinds        = 6
)

// EntityDetailsInstructions is the fenced-protocol guidance added only when
// the controller has enabled the entity runtime.
const EntityDetailsInstructions = `- get_entity_details — find stored runtime data with {"query":"name or topic keywords","kinds":["vision_observation"]} (returns up to 8 minimal candidates), or expand exact returned IDs with {"entity_ids":["ent_00001"],"level":"full"}. Use exactly one selector. Query lookup never expands full payloads. Entity kind/source matters: web results are not image evidence; for a prior screenshot/image use kinds=["vision_observation"], and if none exists report that visual evidence is unavailable. Call it alone.`

type entityDetailsArgs struct {
	EntityIDs []string `json:"entity_ids"`
	Query     string   `json:"query,omitempty"`
	Level     string   `json:"level,omitempty"`
	Kinds     []string `json:"kinds,omitempty"`
}

// decodeEntityDetailsBody parses the fenced JSON form of the controller-only
// entity expansion capability.
func decodeEntityDetailsBody(call *Call) {
	if len(call.Body) > MaxEntityDetailsPayloadBytes {
		call.InputErr = fmt.Sprintf("get_entity_details arguments exceed the %d byte limit", MaxEntityDetailsPayloadBytes)
		return
	}
	var args entityDetailsArgs
	if err := decodeOneJSONObject(call.Body, &args); err != nil {
		call.InputErr = "get_entity_details needs one JSON object in the block body: " + err.Error()
		return
	}
	setEntityIDs(call, args.EntityIDs)
	call.EntityLevel = args.Level
	call.SearchQuery = args.Query
	setEntityKinds(call, args.Kinds)
	if err := ValidateEntityDetailsCall(call); err != nil {
		call.InputErr = err.Error()
	}
}

// ValidateEntityDetailsCall bounds a read-only batch and normalizes its
// requested representation. It grants no permission and performs no lookup.
func ValidateEntityDetailsCall(call *Call) error {
	if call == nil {
		return fmt.Errorf("get_entity_details call is missing")
	}
	call.SearchQuery = strings.TrimSpace(call.SearchQuery)
	call.EntityLevel = strings.ToLower(strings.TrimSpace(call.EntityLevel))
	if call.EntityKindCount < 0 {
		return fmt.Errorf("get_entity_details kind count must not be negative")
	}
	if call.EntityKindCount > MaxEntityDetailsKinds {
		return fmt.Errorf("get_entity_details accepts at most %d kinds", MaxEntityDetailsKinds)
	}
	for index := 0; index < call.EntityKindCount; index++ {
		call.EntityKinds[index] = strings.ToLower(strings.TrimSpace(call.EntityKinds[index]))
		if !entity.ValidKind(entity.Kind(call.EntityKinds[index])) {
			return fmt.Errorf("get_entity_details kind %d is unsupported", index+1)
		}
	}
	if call.EntityIDCount < 0 {
		return fmt.Errorf("get_entity_details entity ID count must not be negative")
	}
	if call.SearchQuery != "" {
		if call.EntityIDCount != 0 {
			return fmt.Errorf("get_entity_details accepts query or entity_ids, not both")
		}
		if len(call.SearchQuery) > MaxEntityQueryBytes {
			return fmt.Errorf("get_entity_details query exceeds %d bytes", MaxEntityQueryBytes)
		}
		if call.EntityLevel == "" {
			call.EntityLevel = "minimal"
		}
		if call.EntityLevel != "minimal" {
			return fmt.Errorf("get_entity_details query returns minimal candidates; use their entity_ids for other detail levels")
		}
		return nil
	}
	if call.EntityIDCount == 0 {
		return fmt.Errorf("get_entity_details needs a query or at least one entity_id")
	}
	if call.EntityIDCount > MaxEntityDetailsIDs {
		return fmt.Errorf("get_entity_details accepts at most %d entity IDs", MaxEntityDetailsIDs)
	}
	for index := 0; index < call.EntityIDCount; index++ {
		id := call.EntityIDs[index]
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("get_entity_details entity_id %d is blank", index+1)
		}
	}
	if call.EntityLevel == "" {
		call.EntityLevel = "full"
	}
	switch call.EntityLevel {
	case "identifier", "minimal", "full":
		return nil
	default:
		return fmt.Errorf("get_entity_details level must be identifier, minimal, or full")
	}
}

func setEntityIDs(call *Call, ids []string) {
	call.EntityIDs = [MaxEntityDetailsIDs]string{}
	call.EntityIDCount = len(ids)
	if call.EntityIDCount > MaxEntityDetailsIDs {
		call.EntityIDCount = len(ids)
		return
	}
	copy(call.EntityIDs[:], ids)
}

func setEntityKinds(call *Call, kinds []string) {
	call.EntityKinds = [MaxEntityDetailsKinds]string{}
	call.EntityKindCount = len(kinds)
	if call.EntityKindCount > MaxEntityDetailsKinds {
		return
	}
	copy(call.EntityKinds[:], kinds)
}
