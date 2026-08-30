package core

import (
	"fmt"
	"net/netip"
	"time"
	"unicode/utf8"
)

// Endpoint is an optional network endpoint extracted by a Parser. A zero value
// means that the source record did not provide an endpoint.
type Endpoint struct {
	IP   netip.Addr
	Port uint16
}

// UserInfo contains the parser-owned user identity declared by a source record.
type UserInfo struct {
	Name string
}

// HTTPInfo contains parser-owned HTTP attributes declared by a source record.
type HTTPInfo struct {
	Method string
	Path   string
	Status int
}

// EventFields is the complete parser-owned portion of a SecurityEvent. It
// deliberately has no system-owned identity, source, parser, or timing fields.
type EventFields struct {
	Timestamp *time.Time
	EventType string
	Source    Endpoint
	Target    Endpoint
	User      *UserInfo
	HTTP      *HTTPInfo
	Service   string
	Labels    map[string]string
	Fields    map[string]any
}

// SecurityEvent combines Guard-owned delivery identity with parser-owned event
// fields. deliveryID is retained privately so Validate can re-derive ID without
// exposing it as part of the frozen logical event model.
type SecurityEvent struct {
	ID EventID

	ObservedAt time.Time
	Timestamp  *time.Time

	NodeID         NodeID
	SourceID       SourceID
	SourcePosition SourcePosition
	ParserID       ParserID
	ParserVersion  ParserVersion
	EmittedIndex   uint32

	EventType string
	Source    Endpoint
	Target    Endpoint
	User      *UserInfo
	HTTP      *HTTPInfo
	Service   string
	Labels    map[string]string
	Fields    map[string]any

	deliveryID DeliveryID
}

// NewSecurityEvent constructs all system-owned fields from a validated
// Delivery and copies the parser-owned containers at their top level.
func NewSecurityEvent(
	nodeID NodeID,
	delivery Delivery,
	parserID ParserID,
	parserVersion ParserVersion,
	emittedIndex uint32,
	fields EventFields,
) (SecurityEvent, error) {
	if err := delivery.Validate(); err != nil {
		return SecurityEvent{}, fmt.Errorf("construct security event: validate delivery: %w", err)
	}
	if !isLowerHex128(string(nodeID)) {
		return SecurityEvent{}, fmt.Errorf("construct security event: node id must be 128-bit lowercase hex")
	}
	if err := validateTextIdentifier("parser id", string(parserID)); err != nil {
		return SecurityEvent{}, fmt.Errorf("construct security event: %w", err)
	}
	if err := validateTextIdentifier("parser version", string(parserVersion)); err != nil {
		return SecurityEvent{}, fmt.Errorf("construct security event: %w", err)
	}
	if err := fields.validate(); err != nil {
		return SecurityEvent{}, fmt.Errorf("construct security event: %w", err)
	}

	eventID, err := SecurityEventID(nodeID, delivery.ID, parserID, parserVersion, emittedIndex)
	if err != nil {
		return SecurityEvent{}, fmt.Errorf("construct security event identity: %w", err)
	}

	event := SecurityEvent{
		ID:             eventID,
		ObservedAt:     delivery.Record.ObservedAt,
		Timestamp:      cloneTime(fields.Timestamp),
		NodeID:         nodeID,
		SourceID:       delivery.Record.SourceID,
		SourcePosition: delivery.Record.Position,
		ParserID:       parserID,
		ParserVersion:  parserVersion,
		EmittedIndex:   emittedIndex,
		EventType:      fields.EventType,
		Source:         fields.Source,
		Target:         fields.Target,
		User:           cloneUserInfo(fields.User),
		HTTP:           cloneHTTPInfo(fields.HTTP),
		Service:        fields.Service,
		Labels:         cloneLabels(fields.Labels),
		Fields:         cloneFields(fields.Fields),
		deliveryID:     delivery.ID,
	}
	return event, nil
}

// Validate checks both the Guard-owned identity binding and parser-owned basic
// field boundaries. It does not interpret parser-specific Fields values.
func (e SecurityEvent) Validate() error {
	if !isLowerHex128(string(e.NodeID)) {
		return fmt.Errorf("security event node id must be 128-bit lowercase hex")
	}
	if !ValidDeliveryID(e.deliveryID) {
		return fmt.Errorf("security event delivery id is not canonical")
	}
	if err := validateTextIdentifier("security event source id", string(e.SourceID)); err != nil {
		return err
	}
	if !e.SourcePosition.Valid() {
		return fmt.Errorf("security event source position is invalid")
	}
	expectedDeliveryID, err := deliveryIDForPosition(e.SourceID, e.SourcePosition)
	if err != nil {
		return fmt.Errorf("derive security event delivery identity: %w", err)
	}
	if e.deliveryID != expectedDeliveryID {
		return fmt.Errorf("security event delivery id does not bind source and position")
	}
	if e.ObservedAt.IsZero() {
		return fmt.Errorf("security event observed time is required")
	}
	if err := validateTextIdentifier("parser id", string(e.ParserID)); err != nil {
		return err
	}
	if err := validateTextIdentifier("parser version", string(e.ParserVersion)); err != nil {
		return err
	}
	expected, err := SecurityEventID(e.NodeID, e.deliveryID, e.ParserID, e.ParserVersion, e.EmittedIndex)
	if err != nil {
		return fmt.Errorf("derive security event identity: %w", err)
	}
	if e.ID != expected {
		return fmt.Errorf("security event id does not bind system-owned fields")
	}
	return (EventFields{
		Timestamp: e.Timestamp,
		EventType: e.EventType,
		Source:    e.Source,
		Target:    e.Target,
		User:      e.User,
		HTTP:      e.HTTP,
		Service:   e.Service,
		Labels:    e.Labels,
		Fields:    e.Fields,
	}).validate()
}

func (fields EventFields) validate() error {
	if fields.Timestamp != nil && fields.Timestamp.IsZero() {
		return fmt.Errorf("event timestamp cannot be zero when present")
	}
	if err := validateTextIdentifier("event type", fields.EventType); err != nil {
		return err
	}
	if err := fields.Source.validate("source endpoint"); err != nil {
		return err
	}
	if err := fields.Target.validate("target endpoint"); err != nil {
		return err
	}
	if fields.User != nil {
		if err := validateTextIdentifier("event user name", fields.User.Name); err != nil {
			return err
		}
	}
	if fields.HTTP != nil {
		if !utf8.ValidString(fields.HTTP.Method) || !utf8.ValidString(fields.HTTP.Path) {
			return fmt.Errorf("event HTTP method and path must be UTF-8")
		}
		if fields.HTTP.Status != 0 && (fields.HTTP.Status < 100 || fields.HTTP.Status > 599) {
			return fmt.Errorf("event HTTP status must be zero or between 100 and 599")
		}
	}
	if !utf8.ValidString(fields.Service) {
		return fmt.Errorf("event service must be UTF-8")
	}
	for key, value := range fields.Labels {
		if err := validateTextIdentifier("event label key", key); err != nil {
			return err
		}
		if !utf8.ValidString(value) {
			return fmt.Errorf("event label %q value must be UTF-8", key)
		}
	}
	for key := range fields.Fields {
		if err := validateTextIdentifier("event field key", key); err != nil {
			return err
		}
	}
	return nil
}

func (endpoint Endpoint) validate(kind string) error {
	if !endpoint.IP.IsValid() {
		if endpoint.Port != 0 {
			return fmt.Errorf("%s port requires an IP address", kind)
		}
		return nil
	}
	if endpoint.IP.Zone() != "" {
		return fmt.Errorf("%s IP zone is not supported", kind)
	}
	return nil
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneUserInfo(value *UserInfo) *UserInfo {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneHTTPInfo(value *HTTPInfo) *HTTPInfo {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneLabels(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func cloneFields(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	cloned := make(map[string]any, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}
