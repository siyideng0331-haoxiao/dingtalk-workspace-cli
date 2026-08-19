package localrunner

import (
	"encoding/binary"
	"encoding/json"
)

type MessageKind string

const (
	MessageText   MessageKind = "text"
	MessageBinary MessageKind = "binary"
)

type EncodedTunnelFrame struct {
	Kind MessageKind
	Data []byte
}

type TunnelCodec struct {
	maxFrameBytes int
}

func NewTunnelCodec(maxFrameBytes int) *TunnelCodec {
	if maxFrameBytes <= 0 {
		maxFrameBytes = DefaultMaxFrameBytes
	}
	return &TunnelCodec{maxFrameBytes: maxFrameBytes}
}

func (c *TunnelCodec) Encode(frame TunnelFrame) (EncodedTunnelFrame, error) {
	if err := frame.Validate(); err != nil {
		return EncodedTunnelFrame{}, err
	}
	if frameIsChunk(frame.Type) {
		return c.encodeBinary(frame)
	}
	return c.encodeText(frame)
}

func (c *TunnelCodec) encodeText(frame TunnelFrame) (EncodedTunnelFrame, error) {
	header, err := json.Marshal(frame.commonHeader())
	if err != nil {
		return EncodedTunnelFrame{}, ErrFrameMalformed
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(header, &fields); err != nil {
		return EncodedTunnelFrame{}, ErrFrameMalformed
	}
	for key, value := range frame.Attributes {
		fields[key] = append(json.RawMessage(nil), value...)
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return EncodedTunnelFrame{}, ErrFrameMalformed
	}
	if len(encoded) > c.maxFrameBytes {
		return EncodedTunnelFrame{}, ErrFrameTooLarge
	}
	return EncodedTunnelFrame{Kind: MessageText, Data: encoded}, nil
}

func (c *TunnelCodec) encodeBinary(frame TunnelFrame) (EncodedTunnelFrame, error) {
	header, err := json.Marshal(frame.commonHeader())
	if err != nil {
		return EncodedTunnelFrame{}, ErrFrameMalformed
	}
	total := 4 + len(header) + len(frame.Payload)
	if total > c.maxFrameBytes {
		return EncodedTunnelFrame{}, ErrFrameTooLarge
	}
	encoded := make([]byte, total)
	binary.BigEndian.PutUint32(encoded[:4], uint32(len(header)))
	copy(encoded[4:], header)
	copy(encoded[4+len(header):], frame.Payload)
	return EncodedTunnelFrame{Kind: MessageBinary, Data: encoded}, nil
}

func (c *TunnelCodec) DecodeText(encoded []byte) (TunnelFrame, error) {
	if len(encoded) == 0 {
		return TunnelFrame{}, ErrFrameMalformed
	}
	if len(encoded) > c.maxFrameBytes {
		return TunnelFrame{}, ErrFrameTooLarge
	}
	frame, err := decodeFrameHeader(encoded)
	if err != nil {
		return TunnelFrame{}, err
	}
	if frameIsChunk(frame.Type) {
		return TunnelFrame{}, ErrFrameTypeMismatch
	}
	if err := frame.Validate(); err != nil {
		return TunnelFrame{}, err
	}
	return frame, nil
}

func (c *TunnelCodec) DecodeBinary(encoded []byte) (TunnelFrame, error) {
	if len(encoded) > c.maxFrameBytes {
		return TunnelFrame{}, ErrFrameTooLarge
	}
	if len(encoded) < 4 {
		return TunnelFrame{}, ErrFrameMalformed
	}
	headerLength := int(binary.BigEndian.Uint32(encoded[:4]))
	if headerLength <= 0 || headerLength > len(encoded)-4 {
		return TunnelFrame{}, ErrFrameMalformed
	}
	frame, err := decodeFrameHeader(encoded[4 : 4+headerLength])
	if err != nil {
		return TunnelFrame{}, err
	}
	if !frameIsChunk(frame.Type) {
		return TunnelFrame{}, ErrFrameTypeMismatch
	}
	if len(frame.Attributes) != 0 {
		return TunnelFrame{}, ErrFrameMalformed
	}
	frame.Payload = append([]byte(nil), encoded[4+headerLength:]...)
	if err := frame.Validate(); err != nil {
		return TunnelFrame{}, err
	}
	return frame, nil
}

func decodeFrameHeader(encoded []byte) (TunnelFrame, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return TunnelFrame{}, ErrFrameMalformed
	}
	var header tunnelCommonHeader
	if err := json.Unmarshal(encoded, &header); err != nil {
		return TunnelFrame{}, ErrFrameMalformed
	}
	for key := range reservedFrameKeys {
		delete(fields, key)
	}
	attributes := make(map[string]json.RawMessage, len(fields))
	for key, value := range fields {
		attributes[key] = append(json.RawMessage(nil), value...)
	}
	return TunnelFrame{
		Version:    header.Version,
		Type:       header.Type,
		RunnerID:   header.RunnerID,
		EndpointID: header.EndpointID,
		RequestID:  header.RequestID,
		Sequence:   header.Sequence,
		Timestamp:  header.Timestamp,
		Attributes: attributes,
	}, nil
}
