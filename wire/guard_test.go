package wire

import (
	"reflect"
	"strings"
	"testing"
)

// The exhaustive set of JSON fields the collector may send on the ingest path
// (Batch and everything reachable from it). Adding a field here is a deliberate
// contract change: update contracts/event-schema.md first (research R10).
var approvedIngestFields = map[string]bool{
	"schema_version":     true,
	"machine_id":         true,
	"client_sent_at":     true,
	"sessions":           true,
	"events":             true,
	"sync_uid":           true,
	"project_key":        true,
	"assistant":          true,
	"model":              true,
	"started_at":         true,
	"ended_at":           true,
	"end_reason":         true,
	"usage":              true,
	"input_tokens":       true,
	"output_tokens":      true,
	"cache_read_tokens":  true,
	"cache_write_tokens": true,
	"event_counts":       true,
	"parent_sync_uid":    true,
	"agent_type":         true,
	"memory_usage":       true,
	"model_usage":        true,
	"scope":              true,
	"memory_id":          true,
	"team_uid":           true,
	"retrieved":          true,
	"cited":              true,
}

// Substrings that must never appear in any outbound field name, on any wire
// type — the categories the spec forbids from ever being transmitted.
var forbiddenNameParts = []string{"path", "remote", "url", "hostname", "username", "detail", "transcript", "display_name", "identity"}

func TestIngestSchemaIsExhaustivelyApproved(t *testing.T) {
	fields := map[string]bool{}
	collectJSONFields(t, reflect.TypeOf(Batch{}), fields, map[reflect.Type]bool{})
	for f := range fields {
		if !approvedIngestFields[f] {
			t.Errorf("wire field %q is not in the approved ingest schema; update contracts/event-schema.md deliberately first", f)
		}
	}
	for f := range approvedIngestFields {
		if !fields[f] {
			t.Errorf("approved field %q missing from wire structs; contract and code drifted", f)
		}
	}
}

func TestNoOutboundFieldNamesForbiddenData(t *testing.T) {
	outbound := []reflect.Type{
		reflect.TypeOf(Batch{}),
		reflect.TypeOf(HeartbeatRequest{}),
		reflect.TypeOf(DeviceStartRequest{}),
		reflect.TypeOf(DeviceTokenRequest{}),
		reflect.TypeOf(LinkValidateRequest{}),
		reflect.TypeOf(LinkAutoCreateRequest{}),
	}
	for _, typ := range outbound {
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

func TestBatchEventsStayReserved(t *testing.T) {
	f, ok := reflect.TypeOf(Batch{}).FieldByName("Events")
	if !ok {
		t.Fatal("Batch.Events removed; contract requires the reserved array")
	}
	if f.Type.Elem().NumField() != 0 {
		t.Error("Batch.Events element gained fields; v1 reserves it empty — this needs a schema version bump")
	}
}

func collectJSONFields(t *testing.T, typ reflect.Type, out map[string]bool, seen map[reflect.Type]bool) {
	t.Helper()
	for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Map {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct || seen[typ] {
		return
	}
	seen[typ] = true
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		tag := strings.Split(field.Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			t.Errorf("%s.%s has no explicit json tag; wire fields must be deliberate", typ.Name(), field.Name)
			continue
		}
		out[tag] = true
		collectJSONFields(t, field.Type, out, seen)
	}
}
