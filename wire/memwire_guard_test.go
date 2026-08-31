package wire

import (
	"reflect"
	"strings"
	"testing"
)

// The exhaustive set of JSON fields the memory pipeline may carry in either
// direction (contracts/memory-sync-api.md). Adding a field means updating the
// contract first. Memory content lives here — never on the ingest path.
var approvedMemoryFields = map[string]bool{
	"project_key":     true,
	"entries":         true,
	"uid":             true,
	"content":         true,
	"kind":            true,
	"origin":          true,
	"priority":        true,
	"captured_at":     true,
	"branch":          true,
	"commit_hash":     true,
	"supersedes":      true,
	"results":         true,
	"status":          true,
	"reason":          true,
	"action":          true,
	"disclosure":      true,
	"author":          true,
	"author_former":   true,
	"contradicts_uid": true,
	"flagged":         true,
	"mine":            true,
	"edited":          true,
	"next_cursor":     true,
	"more":            true,
	"resync":          true,
	"updated_at":      true,
	"memory_endpoint": true,
}

var memoryTypes = []reflect.Type{
	reflect.TypeOf(MemoryContributeRequest{}),
	reflect.TypeOf(MemoryContributeResponse{}),
	reflect.TypeOf(MemoryConsentRequest{}),
	reflect.TypeOf(MemoryConsentResponse{}),
	reflect.TypeOf(MemoryPullResponse{}),
}

func TestMemorySchemaIsExhaustivelyApproved(t *testing.T) {
	fields := map[string]bool{}
	for _, typ := range memoryTypes {
		collectJSONFields(t, typ, fields, map[reflect.Type]bool{})
	}
	for f := range fields {
		if !approvedMemoryFields[f] {
			t.Errorf("memory wire field %q is not approved; update contracts/memory-sync-api.md deliberately first", f)
		}
	}
	for f := range approvedMemoryFields {
		if !fields[f] {
			t.Errorf("approved memory field %q missing from wire structs; contract and code drifted", f)
		}
	}
}

func TestMemoryTypesRespectForbiddenNames(t *testing.T) {
	for _, typ := range memoryTypes {
		fields := map[string]bool{}
		collectJSONFields(t, typ, fields, map[reflect.Type]bool{})
		for f := range fields {
			for _, bad := range forbiddenNameParts {
				if strings.Contains(f, bad) {
					t.Errorf("%s carries field %q which suggests forbidden data (%q)", typ.Name(), f, bad)
				}
			}
		}
	}
}

// The two pipelines must not share types: nothing reachable from Batch may be a
// memory type, and nothing reachable from a memory type may be Batch. If they
// ever merge, memory content could ride an ingest batch (Constitution II).
func TestMemoryAndIngestPipelinesAreDisjoint(t *testing.T) {
	batchTypes := reachableStructs(reflect.TypeOf(Batch{}))
	for _, mt := range memoryTypes {
		if batchTypes[mt] {
			t.Errorf("memory type %s is reachable from wire.Batch; pipelines must stay disjoint", mt.Name())
		}
	}
	batchType := reflect.TypeOf(Batch{})
	for _, mt := range memoryTypes {
		if reachableStructs(mt)[batchType] {
			t.Errorf("wire.Batch is reachable from memory type %s; pipelines must stay disjoint", mt.Name())
		}
	}
}

func reachableStructs(typ reflect.Type) map[reflect.Type]bool {
	seen := map[reflect.Type]bool{}
	var walk func(reflect.Type)
	walk = func(t reflect.Type) {
		for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Map || t.Kind() == reflect.Array {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct || seen[t] {
			return
		}
		seen[t] = true
		for i := 0; i < t.NumField(); i++ {
			walk(t.Field(i).Type)
		}
	}
	walk(typ)
	return seen
}
