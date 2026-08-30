package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"strings"
	"unicode/utf8"
)

var base32HexNoPadding = base32.HexEncoding.WithPadding(base32.NoPadding)

// FileDeliveryID derives the stable identity for one file record range.
func FileDeliveryID(sourceID SourceID, position FilePosition) (DeliveryID, error) {
	if err := validateTextIdentifier("source id", string(sourceID)); err != nil {
		return "", err
	}
	if !isLowerHex128(position.Generation) {
		return "", fmt.Errorf("file generation must be 128-bit lowercase hex")
	}
	if position.StartOffset > position.EndOffset {
		return "", fmt.Errorf("file position start %d exceeds end %d", position.StartOffset, position.EndOffset)
	}

	var frame bytes.Buffer
	frame.WriteString("guard.delivery.file.v1\x00")
	writeFramedString(&frame, string(sourceID))
	writeFramedString(&frame, position.Generation)
	writeUint64(&frame, position.StartOffset)
	writeUint64(&frame, position.EndOffset)
	return DeliveryID(hashIdentity("dlv1_", frame.Bytes())), nil
}

// JournaldDeliveryID derives the stable identity for one opaque journal cursor.
func JournaldDeliveryID(sourceID SourceID, cursor string) (DeliveryID, error) {
	if err := validateTextIdentifier("source id", string(sourceID)); err != nil {
		return "", err
	}
	if cursor == "" || !utf8.ValidString(cursor) {
		return "", fmt.Errorf("journald cursor must be non-empty UTF-8")
	}

	var frame bytes.Buffer
	frame.WriteString("guard.delivery.journald.v1\x00")
	writeFramedString(&frame, string(sourceID))
	writeFramedString(&frame, cursor)
	return DeliveryID(hashIdentity("dlv1_", frame.Bytes())), nil
}

// SecurityEventID derives the stable event identity from system-owned fields.
func SecurityEventID(
	nodeID NodeID,
	deliveryID DeliveryID,
	parserID ParserID,
	parserVersion ParserVersion,
	emittedIndex uint32,
) (EventID, error) {
	if !isLowerHex128(string(nodeID)) {
		return "", fmt.Errorf("node id must be 128-bit lowercase hex")
	}
	if !ValidDeliveryID(deliveryID) {
		return "", fmt.Errorf("delivery id is not canonical")
	}
	if err := validateTextIdentifier("parser id", string(parserID)); err != nil {
		return "", err
	}
	if err := validateTextIdentifier("parser version", string(parserVersion)); err != nil {
		return "", err
	}

	var frame bytes.Buffer
	frame.WriteString("guard.security-event.v1\x00")
	writeFramedString(&frame, string(nodeID))
	writeFramedString(&frame, string(deliveryID))
	writeFramedString(&frame, string(parserID))
	writeFramedString(&frame, string(parserVersion))
	writeUint32(&frame, emittedIndex)
	return EventID(hashIdentity("evt1_", frame.Bytes())), nil
}

func writeFramedString(buffer *bytes.Buffer, value string) {
	writeUint32(buffer, uint32(len([]byte(value))))
	buffer.WriteString(value)
}

func writeUint32(buffer *bytes.Buffer, value uint32) {
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], value)
	buffer.Write(encoded[:])
}

func writeUint64(buffer *bytes.Buffer, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	buffer.Write(encoded[:])
}

func hashIdentity(prefix string, frame []byte) string {
	digest := sha256.Sum256(frame)
	return prefix + strings.ToLower(base32HexNoPadding.EncodeToString(digest[:]))
}

func isLowerHex128(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

// ValidDeliveryID reports whether an ID uses the frozen dlv1 encoding.
func ValidDeliveryID(value DeliveryID) bool {
	return validHashedIdentity(string(value), "dlv1_")
}

// ValidEventID reports whether an ID uses the frozen evt1 encoding.
func ValidEventID(value EventID) bool {
	return validHashedIdentity(string(value), "evt1_")
}

func validHashedIdentity(value, prefix string) bool {
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+52 {
		return false
	}
	for _, character := range value[len(prefix):] {
		if character >= '0' && character <= '9' {
			continue
		}
		if character < 'a' || character > 'v' {
			return false
		}
	}
	payload := value[len(prefix):]
	decoded, err := base32HexNoPadding.DecodeString(strings.ToUpper(payload))
	if err != nil || len(decoded) != sha256.Size {
		return false
	}
	return strings.ToLower(base32HexNoPadding.EncodeToString(decoded)) == payload
}

func validateTextIdentifier(kind, value string) error {
	if value == "" || !utf8.ValidString(value) {
		return fmt.Errorf("%s must be non-empty UTF-8", kind)
	}
	return nil
}
