package core

import (
	"testing"
	"time"
)

func TestDetectionTerminalOutcomeValidate(t *testing.T) {
	delivery := securityEventTestDelivery(t)
	eventID, err := SecurityEventID(
		"00112233445566778899aabbccddeeff", delivery.ID, "parser-1", "v1", 0,
	)
	if err != nil {
		t.Fatal(err)
	}
	base := DetectionTerminalOutcome{
		DeliveryID: delivery.ID, EventID: eventID, RuleID: "rule-1", RuleVersion: "v1",
		Kind: DetectionOutcomeSuccess, CompletedAt: time.Unix(1_700_000_001, 0).UTC(),
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("success Validate(): %v", err)
	}
	permanent := base
	permanent.Kind = DetectionOutcomeRecordPermanent
	permanent.FailureCode = "invalid_rule_input"
	if err := permanent.Validate(); err != nil {
		t.Fatalf("permanent Validate(): %v", err)
	}
	for name, mutate := range map[string]func(*DetectionTerminalOutcome){
		"success with failure": func(value *DetectionTerminalOutcome) { value.FailureCode = "unexpected" },
		"permanent without failure": func(value *DetectionTerminalOutcome) {
			value.Kind = DetectionOutcomeRecordPermanent
		},
		"unknown kind": func(value *DetectionTerminalOutcome) { value.Kind = 99 },
		"failure code too long": func(value *DetectionTerminalOutcome) {
			value.Kind = DetectionOutcomeRecordPermanent
			value.FailureCode = string(make([]byte, 129))
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := base
			mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}
