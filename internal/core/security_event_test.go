package core

import (
	"net/netip"
	"testing"
	"time"
)

const securityEventTestNodeID NodeID = "00112233445566778899aabbccddeeff"

func TestNewSecurityEventOwnsSystemFields(t *testing.T) {
	delivery := securityEventTestDelivery(t)
	timestamp := time.Unix(90, 0).UTC()
	fields := EventFields{
		Timestamp: &timestamp,
		EventType: "authentication_failure",
		Source:    Endpoint{IP: netip.MustParseAddr("192.0.2.10"), Port: 55123},
		Target:    Endpoint{IP: netip.MustParseAddr("198.51.100.20"), Port: 22},
		User:      &UserInfo{Name: "alice"},
		HTTP:      &HTTPInfo{Method: "POST", Path: "/login", Status: 401},
		Service:   "sshd",
		Labels:    map[string]string{"environment": "test"},
		Fields:    map[string]any{"attempt": 3},
	}

	event, err := NewSecurityEvent(securityEventTestNodeID, delivery, "parser-auth", "v1", 7, fields)
	if err != nil {
		t.Fatal(err)
	}
	wantID, err := SecurityEventID(securityEventTestNodeID, delivery.ID, "parser-auth", "v1", 7)
	if err != nil {
		t.Fatal(err)
	}
	if event.ID != wantID || event.NodeID != securityEventTestNodeID ||
		event.SourceID != delivery.Record.SourceID || event.SourcePosition != delivery.Record.Position ||
		event.ObservedAt != delivery.Record.ObservedAt || event.ParserID != "parser-auth" ||
		event.ParserVersion != "v1" || event.EmittedIndex != 7 {
		t.Fatalf("system-owned fields were not derived from constructor inputs: %+v", event)
	}
	if err := event.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestNewSecurityEventStableIdentityForMultipleOutputs(t *testing.T) {
	delivery := securityEventTestDelivery(t)
	fields := EventFields{EventType: "network_connection"}

	first, err := NewSecurityEvent(securityEventTestNodeID, delivery, "parser-network", "v2", 0, fields)
	if err != nil {
		t.Fatal(err)
	}
	firstAgain, err := NewSecurityEvent(securityEventTestNodeID, delivery, "parser-network", "v2", 0, fields)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewSecurityEvent(securityEventTestNodeID, delivery, "parser-network", "v2", 1, fields)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != firstAgain.ID {
		t.Fatalf("same emitted index produced unstable IDs: %q != %q", first.ID, firstAgain.ID)
	}
	if first.ID == second.ID {
		t.Fatalf("different emitted indexes produced the same ID %q", first.ID)
	}
}

func TestNewSecurityEventClonesParserOwnedContainers(t *testing.T) {
	delivery := securityEventTestDelivery(t)
	timestamp := time.Unix(91, 0).UTC()
	user := &UserInfo{Name: "alice"}
	http := &HTTPInfo{Method: "GET", Path: "/health", Status: 200}
	labels := map[string]string{"role": "admin"}
	values := map[string]any{"count": 1}

	event, err := NewSecurityEvent(securityEventTestNodeID, delivery, "parser-http", "v1", 0, EventFields{
		Timestamp: &timestamp,
		EventType: "http_request",
		User:      user,
		HTTP:      http,
		Labels:    labels,
		Fields:    values,
	})
	if err != nil {
		t.Fatal(err)
	}

	timestamp = timestamp.Add(time.Hour)
	user.Name = "mallory"
	http.Method = "DELETE"
	labels["role"] = "guest"
	values["count"] = 2
	if event.Timestamp.Equal(timestamp) || event.User.Name != "alice" || event.HTTP.Method != "GET" ||
		event.Labels["role"] != "admin" || event.Fields["count"] != 1 {
		t.Fatalf("constructor retained caller-owned parser containers: %+v", event)
	}
}

func TestSecurityEventRejectsInvalidBoundaries(t *testing.T) {
	delivery := securityEventTestDelivery(t)
	validFields := EventFields{EventType: "authentication_failure"}

	tests := []struct {
		name   string
		build  func() (SecurityEvent, error)
		mutate func(*SecurityEvent)
	}{
		{
			name: "node id",
			build: func() (SecurityEvent, error) {
				return NewSecurityEvent("not-a-node", delivery, "parser-1", "v1", 0, validFields)
			},
		},
		{
			name: "parser id",
			build: func() (SecurityEvent, error) {
				return NewSecurityEvent(securityEventTestNodeID, delivery, "", "v1", 0, validFields)
			},
		},
		{
			name: "parser version",
			build: func() (SecurityEvent, error) {
				return NewSecurityEvent(securityEventTestNodeID, delivery, "parser-1", "", 0, validFields)
			},
		},
		{
			name: "event type",
			build: func() (SecurityEvent, error) {
				return NewSecurityEvent(securityEventTestNodeID, delivery, "parser-1", "v1", 0, EventFields{})
			},
		},
		{
			name: "HTTP status",
			build: func() (SecurityEvent, error) {
				return NewSecurityEvent(securityEventTestNodeID, delivery, "parser-1", "v1", 0, EventFields{
					EventType: "http_request",
					HTTP:      &HTTPInfo{Status: 99},
				})
			},
		},
		{
			name: "endpoint port without IP",
			build: func() (SecurityEvent, error) {
				return NewSecurityEvent(securityEventTestNodeID, delivery, "parser-1", "v1", 0, EventFields{
					EventType: "network_connection",
					Source:    Endpoint{Port: 22},
				})
			},
		},
		{
			name: "tampered event id",
			build: func() (SecurityEvent, error) {
				return NewSecurityEvent(securityEventTestNodeID, delivery, "parser-1", "v1", 0, validFields)
			},
			mutate: func(event *SecurityEvent) {
				event.ID = "evt1_0000000000000000000000000000000000000000000000000000"
			},
		},
		{
			name: "tampered parser identity",
			build: func() (SecurityEvent, error) {
				return NewSecurityEvent(securityEventTestNodeID, delivery, "parser-1", "v1", 0, validFields)
			},
			mutate: func(event *SecurityEvent) {
				event.ParserVersion = "v2"
			},
		},
		{
			name: "tampered source position",
			build: func() (SecurityEvent, error) {
				return NewSecurityEvent(securityEventTestNodeID, delivery, "parser-1", "v1", 0, validFields)
			},
			mutate: func(event *SecurityEvent) {
				position, err := NewFilePosition(FilePosition{
					Generation:  "ffeeddccbbaa99887766554433221100",
					DeviceID:    10,
					Inode:       20,
					StartOffset: 120,
					EndOffset:   140,
				})
				if err != nil {
					panic(err)
				}
				event.SourcePosition = position
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event, err := test.build()
			if err != nil {
				return
			}
			if test.mutate != nil {
				test.mutate(&event)
			}
			if err := event.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func securityEventTestDelivery(t *testing.T) Delivery {
	t.Helper()
	position, err := NewFilePosition(FilePosition{
		Generation:  "ffeeddccbbaa99887766554433221100",
		DeviceID:    10,
		Inode:       20,
		StartOffset: 100,
		EndOffset:   120,
	})
	if err != nil {
		t.Fatal(err)
	}
	deliveryID, err := FileDeliveryID("source-security", FilePosition{
		Generation:  "ffeeddccbbaa99887766554433221100",
		DeviceID:    10,
		Inode:       20,
		StartOffset: 100,
		EndOffset:   120,
	})
	if err != nil {
		t.Fatal(err)
	}
	return Delivery{
		ID:       deliveryID,
		Sequence: 1,
		Record: RawRecord{
			SourceID:   "source-security",
			ObservedAt: time.Unix(100, 0).UTC(),
			Position:   position,
			Content:    []byte("fixture"),
		},
	}
}
