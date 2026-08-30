package core

import (
	"net/netip"
	"testing"
	"time"
)

func TestProcessingSemanticModelsValidateClosedCombinations(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	deliveryID, err := FileDeliveryID("source-1", FilePosition{
		Generation: "00112233445566778899aabbccddeeff", StartOffset: 0, EndOffset: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	eventID, err := SecurityEventID("00112233445566778899aabbccddeeff", deliveryID, "parser-1", "v1", 0)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		valid   func() error
		wantErr bool
	}{
		{
			name: "parser success",
			valid: func() error {
				return (ParserTerminalOutcome{
					DeliveryID: deliveryID, ParserID: "parser-1", ParserVersion: "v1",
					Kind: ParserOutcomeSuccess, EmittedCount: 1, CompletedAt: now,
				}).Validate()
			},
		},
		{
			name: "no match cannot emit",
			valid: func() error {
				return (ParserTerminalOutcome{
					DeliveryID: deliveryID, ParserID: "parser-1", ParserVersion: "v1",
					Kind: ParserOutcomeNoMatch, EmittedCount: 1, CompletedAt: now,
				}).Validate()
			},
			wantErr: true,
		},
		{
			name: "no match",
			valid: func() error {
				return (ParserTerminalOutcome{
					DeliveryID: deliveryID, ParserID: "parser-1", ParserVersion: "v1",
					Kind: ParserOutcomeNoMatch, CompletedAt: now,
				}).Validate()
			},
		},
		{
			name: "record permanent",
			valid: func() error {
				return (ParserTerminalOutcome{
					DeliveryID: deliveryID, ParserID: "parser-1", ParserVersion: "v1",
					Kind: ParserOutcomeRecordPermanent, FailureCode: "malformed", CompletedAt: now,
				}).Validate()
			},
		},
		{
			name: "detection membership",
			valid: func() error {
				return (DetectionContribution{
					DeliveryID: deliveryID, EventID: eventID, RuleID: "rule-1", RuleVersion: "v1", ContributedAt: now,
				}).Validate()
			},
		},
		{
			name: "alert target must be canonical",
			valid: func() error {
				return (Alert{
					ID: "alert-1", NodeID: "00112233445566778899aabbccddeeff", EventID: eventID,
					RuleID: "rule-1", RuleVersion: "v1", CanonicalTarget: netip.MustParsePrefix("192.0.2.1/24"),
					ObservedAt: now, CreatedAt: now,
				}).Validate()
			},
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.valid()
			if (err != nil) != test.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}
