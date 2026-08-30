package core

import "testing"

func TestIdentityGoldenVectors(t *testing.T) {
	nodeID := NodeID("00112233445566778899aabbccddeeff")
	tests := []struct {
		name         string
		delivery     func() (DeliveryID, error)
		parserID     ParserID
		version      ParserVersion
		emittedIndex uint32
		wantDelivery DeliveryID
		wantEvent    EventID
	}{
		{
			name: "file",
			delivery: func() (DeliveryID, error) {
				return FileDeliveryID("source-ssh-auth", FilePosition{
					Generation:  "0123456789abcdef0123456789abcdef",
					StartOffset: 4096,
					EndOffset:   4173,
				})
			},
			parserID:     "parser-sshd",
			version:      "v1",
			emittedIndex: 0,
			wantDelivery: "dlv1_oghspfuq9jlm1q5pf1c5kktm4da0e053mn4dnotbd33jjf5be300",
			wantEvent:    "evt1_5i244qf55nhq4k9idbql46fd9djlhfgk0jtge69j3bl25ssqnjo0",
		},
		{
			name: "journald",
			delivery: func() (DeliveryID, error) {
				return JournaldDeliveryID(
					"source-journald",
					"s=0123456789abcdef;i=000000000000002a;b=feedface;m=0000000000100000;t=0000000000200000;x=bead",
				)
			},
			parserID:     "parser-systemd",
			version:      "2026-08-30",
			emittedIndex: 2,
			wantDelivery: "dlv1_gg0g54nqcpa8qbe8kap3osfjq01aco7fi1kuuh9m4166id2cq37g",
			wantEvent:    "evt1_r7l9ona30tue9bbbek4q17jf23fkllccohfo88h4bsnkcc1go3d0",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deliveryID, err := test.delivery()
			if err != nil {
				t.Fatalf("delivery id: %v", err)
			}
			if deliveryID != test.wantDelivery {
				t.Fatalf("delivery id = %q, want %q", deliveryID, test.wantDelivery)
			}
			eventID, err := SecurityEventID(nodeID, deliveryID, test.parserID, test.version, test.emittedIndex)
			if err != nil {
				t.Fatalf("event id: %v", err)
			}
			if eventID != test.wantEvent {
				t.Fatalf("event id = %q, want %q", eventID, test.wantEvent)
			}
		})
	}
}

func TestIdentityRejectsNonCanonicalInputs(t *testing.T) {
	if _, err := FileDeliveryID("source", FilePosition{Generation: "ABC", EndOffset: 1}); err == nil {
		t.Fatal("invalid generation was accepted")
	}
	if _, err := JournaldDeliveryID("source", ""); err == nil {
		t.Fatal("empty cursor was accepted")
	}
	if _, err := SecurityEventID(
		"00112233445566778899aabbccddeeff",
		"dlv1_not-canonical",
		"parser",
		"v1",
		0,
	); err == nil {
		t.Fatal("non-canonical delivery id was accepted")
	}
	if ValidDeliveryID("dlv1_not-canonical") || ValidEventID("evt1_not-canonical") {
		t.Fatal("invalid hashed identity passed validation")
	}
}

func TestHashedIdentityRejectsNonCanonicalPaddingBits(t *testing.T) {
	id, err := JournaldDeliveryID("source-1", "cursor-1")
	if err != nil {
		t.Fatal(err)
	}
	mutated := []byte(id)
	if mutated[len(mutated)-1] == '0' {
		mutated[len(mutated)-1] = '1'
	} else {
		mutated[len(mutated)-1] = 'h'
	}
	if ValidDeliveryID(DeliveryID(mutated)) {
		t.Fatal("delivery id with non-canonical padding bits was accepted")
	}
}
