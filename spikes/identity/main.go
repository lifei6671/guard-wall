// Command identity-spike verifies M0 Delivery ID and Event ID golden vectors.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type vector struct {
	Name               string `json:"name"`
	DeliveryID         string `json:"delivery_id"`
	EventID            string `json:"event_id"`
	ExpectedDeliveryID string `json:"expected_delivery_id"`
	ExpectedEventID    string `json:"expected_event_id"`
}

func writeField(buffer *bytes.Buffer, value string) {
	if err := binary.Write(buffer, binary.BigEndian, uint32(len([]byte(value)))); err != nil {
		panic(err)
	}
	buffer.WriteString(value)
}

func hashID(prefix string, frame []byte) string {
	digest := sha256.Sum256(frame)
	encoded := base32.HexEncoding.WithPadding(base32.NoPadding).EncodeToString(digest[:])
	return prefix + strings.ToLower(encoded)
}

func fileDeliveryID(sourceID, generation string, startOffset, endOffset uint64) string {
	var frame bytes.Buffer
	frame.WriteString("guard.delivery.file.v1\x00")
	writeField(&frame, sourceID)
	writeField(&frame, generation)
	if err := binary.Write(&frame, binary.BigEndian, startOffset); err != nil {
		panic(err)
	}
	if err := binary.Write(&frame, binary.BigEndian, endOffset); err != nil {
		panic(err)
	}
	return hashID("dlv1_", frame.Bytes())
}

func journaldDeliveryID(sourceID, cursor string) string {
	var frame bytes.Buffer
	frame.WriteString("guard.delivery.journald.v1\x00")
	writeField(&frame, sourceID)
	writeField(&frame, cursor)
	return hashID("dlv1_", frame.Bytes())
}

func eventID(nodeID, deliveryID, parserID, parserVersion string, emittedIndex uint32) string {
	var frame bytes.Buffer
	frame.WriteString("guard.security-event.v1\x00")
	writeField(&frame, nodeID)
	writeField(&frame, deliveryID)
	writeField(&frame, parserID)
	writeField(&frame, parserVersion)
	if err := binary.Write(&frame, binary.BigEndian, emittedIndex); err != nil {
		panic(err)
	}
	return hashID("evt1_", frame.Bytes())
}

func vectors() []vector {
	nodeID := "00112233445566778899aabbccddeeff"
	fileDelivery := fileDeliveryID(
		"source-ssh-auth",
		"0123456789abcdef0123456789abcdef",
		4096,
		4173,
	)
	journalDelivery := journaldDeliveryID(
		"source-journald",
		"s=0123456789abcdef;i=000000000000002a;b=feedface;m=0000000000100000;t=0000000000200000;x=bead",
	)
	return []vector{
		{
			Name:               "file",
			DeliveryID:         fileDelivery,
			EventID:            eventID(nodeID, fileDelivery, "parser-sshd", "v1", 0),
			ExpectedDeliveryID: "dlv1_oghspfuq9jlm1q5pf1c5kktm4da0e053mn4dnotbd33jjf5be300",
			ExpectedEventID:    "evt1_5i244qf55nhq4k9idbql46fd9djlhfgk0jtge69j3bl25ssqnjo0",
		},
		{
			Name:               "journald",
			DeliveryID:         journalDelivery,
			EventID:            eventID(nodeID, journalDelivery, "parser-systemd", "2026-08-30", 2),
			ExpectedDeliveryID: "dlv1_gg0g54nqcpa8qbe8kap3osfjq01aco7fi1kuuh9m4166id2cq37g",
			ExpectedEventID:    "evt1_r7l9ona30tue9bbbek4q17jf23fkllccohfo88h4bsnkcc1go3d0",
		},
	}
}

func main() {
	items := vectors()
	generate := len(os.Args) == 2 && os.Args[1] == "generate"
	if !generate {
		for _, item := range items {
			if item.DeliveryID != item.ExpectedDeliveryID || item.EventID != item.ExpectedEventID {
				fmt.Fprintf(os.Stderr, "%s golden vector mismatch\n", item.Name)
				os.Exit(1)
			}
		}
	}
	result := map[string]any{
		"status":  map[bool]string{true: "GENERATED", false: "PASS"}[generate],
		"vectors": items,
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		panic(err)
	}
}
