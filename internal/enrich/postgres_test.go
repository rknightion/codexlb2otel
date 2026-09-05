package enrich

import (
	"fmt"
	"strings"
	"testing"
)

func TestScanRow_MapsNullableProxyAndUpstreamColumns(t *testing.T) {
	zero := 0
	queue := 125
	bridge := 2
	status := 503
	empty := ""
	errorCode := "upstream_overloaded"
	transport := "http"

	tests := []struct {
		name      string
		queue     *int
		gate      *int
		bridge    *int
		status    *int
		errorCode *string
		transport *string
	}{
		{
			name:      "present",
			queue:     &queue,
			gate:      &zero,
			bridge:    &bridge,
			status:    &status,
			errorCode: &errorCode,
			transport: &transport,
		},
		{
			name:      "zero and empty",
			queue:     &zero,
			gate:      &zero,
			bridge:    &zero,
			status:    &zero,
			errorCode: &empty,
			transport: &empty,
		},
		{
			name: "null",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := scanRow(&fakeRowScanner{values: scanValues(tt.queue, tt.gate, tt.bridge, tt.status, tt.errorCode, tt.transport)})
			if err != nil {
				t.Fatalf("scanRow() error = %v", err)
			}
			assertOptionalInt(t, got.LatencyQueueMS, tt.queue)
			assertOptionalInt(t, got.LatencyResponseCreateGateWaitMS, tt.gate)
			assertOptionalInt(t, got.LatencyBridgeQueueWaitMS, tt.bridge)
			wantStatus := 0
			if tt.status != nil {
				wantStatus = *tt.status
			}
			if got.UpstreamStatusCode != wantStatus {
				t.Fatalf("UpstreamStatusCode = %d, want %d", got.UpstreamStatusCode, wantStatus)
			}
			wantErrorCode := ""
			if tt.errorCode != nil {
				wantErrorCode = *tt.errorCode
			}
			if got.UpstreamErrorCode != wantErrorCode {
				t.Fatalf("UpstreamErrorCode = %q, want %q", got.UpstreamErrorCode, wantErrorCode)
			}
			wantTransport := ""
			if tt.transport != nil {
				wantTransport = *tt.transport
			}
			if got.UpstreamTransport != wantTransport {
				t.Fatalf("UpstreamTransport = %q, want %q", got.UpstreamTransport, wantTransport)
			}
		})
	}
}

func TestPostgresQueriesSelectAllEnrichmentColumns(t *testing.T) {
	columns := []string{
		"latency_queue_ms",
		"latency_response_create_gate_wait_ms",
		"latency_bridge_queue_wait_ms",
		"upstream_status_code",
		"upstream_error_code",
		"upstream_transport",
	}
	for _, query := range []struct {
		name string
		sql  string
	}{
		{name: "lookup", sql: lookupSQL},
		{name: "prefetch", sql: prefetchSQL},
	} {
		t.Run(query.name, func(t *testing.T) {
			for _, column := range columns {
				needle := "request_logs." + column
				if count := strings.Count(query.sql, needle); count != 1 {
					t.Errorf("column %q occurs %d times, want once", needle, count)
				}
			}
		})
	}
}

func scanValues(queue, gate, bridge, status *int, errorCode, transport *string) []any {
	return []any{
		int64(7), "resp_scan", "archive_scan", float64(1.25), "key-scan", "scan",
		"success", "", "upstream", float64(12.5), float64(8.25),
		optionalIntValue(queue), optionalIntValue(gate), optionalIntValue(bridge),
		optionalIntValue(status), optionalStringValue(errorCode), optionalStringValue(transport),
	}
}

func optionalIntValue(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func optionalStringValue(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func assertOptionalInt(t *testing.T, got, want *int) {
	t.Helper()
	if got == nil || want == nil {
		if got != nil || want != nil {
			t.Fatalf("optional int = %v, want %v", got, want)
		}
		return
	}
	if *got != *want {
		t.Fatalf("optional int = %d, want %d", *got, *want)
	}
}

func intPtr(value int) *int {
	return &value
}

type fakeRowScanner struct {
	values []any
}

func (s *fakeRowScanner) Scan(dest ...any) error {
	if len(dest) != len(s.values) {
		return fmt.Errorf("Scan() received %d destinations, want %d", len(dest), len(s.values))
	}
	for i, value := range s.values {
		switch dst := dest[i].(type) {
		case *int64:
			var ok bool
			*dst, ok = value.(int64)
			if !ok {
				return fmt.Errorf("value %d has type %T, want int64", i, value)
			}
		case *string:
			var ok bool
			*dst, ok = value.(string)
			if !ok {
				return fmt.Errorf("value %d has type %T, want string", i, value)
			}
		case **float64:
			if value == nil {
				*dst = nil
				continue
			}
			v, ok := value.(float64)
			if !ok {
				return fmt.Errorf("value %d has type %T, want float64", i, value)
			}
			*dst = &v
		case *float64:
			var ok bool
			*dst, ok = value.(float64)
			if !ok {
				return fmt.Errorf("value %d has type %T, want float64", i, value)
			}
		case **int:
			if value == nil {
				*dst = nil
				continue
			}
			v, ok := value.(int)
			if !ok {
				return fmt.Errorf("value %d has type %T, want int", i, value)
			}
			*dst = &v
		case **string:
			if value == nil {
				*dst = nil
				continue
			}
			v, ok := value.(string)
			if !ok {
				return fmt.Errorf("value %d has type %T, want string", i, value)
			}
			*dst = &v
		default:
			return fmt.Errorf("destination %d has unsupported type %T", i, dest[i])
		}
	}
	return nil
}
