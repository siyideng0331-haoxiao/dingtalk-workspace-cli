package localrunner

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"testing"
)

func TestControlFrameRoundTripsAsText(t *testing.T) {
	frame := validFrame(FrameRequestStart)
	frame.Attributes = map[string]json.RawMessage{
		"method": json.RawMessage(`"POST"`),
		"path":   json.RawMessage(`"/rpc"`),
	}

	codec := NewTunnelCodec(DefaultMaxFrameBytes)
	encoded, err := codec.Encode(frame)
	if err != nil {
		t.Fatal(err)
	}
	if encoded.Kind != MessageText {
		t.Fatalf("kind = %v, want text", encoded.Kind)
	}
	decoded, err := codec.DecodeText(encoded.Data)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Type != frame.Type || decoded.RunnerID != frame.RunnerID || decoded.EndpointID != frame.EndpointID || decoded.RequestID != frame.RequestID {
		t.Fatal("control frame metadata changed")
	}
	if string(decoded.Attributes["method"]) != `"POST"` || string(decoded.Attributes["path"]) != `"/rpc"` {
		t.Fatal("control frame attributes changed")
	}
}

func TestResponseChunkRoundTripsOpaqueBinary(t *testing.T) {
	payload := []byte{0xff, 0x00, 0xfe, '\n'}
	frame := validFrame(FrameResponseChunk)
	frame.Payload = payload

	codec := NewTunnelCodec(DefaultMaxFrameBytes)
	encoded, err := codec.Encode(frame)
	if err != nil {
		t.Fatal(err)
	}
	if encoded.Kind != MessageBinary {
		t.Fatalf("kind = %v, want binary", encoded.Kind)
	}
	headerLength := binary.BigEndian.Uint32(encoded.Data[:4])
	if headerLength == 0 || int(headerLength) >= len(encoded.Data) {
		t.Fatal("binary header length is invalid")
	}
	decoded, err := codec.DecodeBinary(encoded.Data)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Type != frame.Type || !bytes.Equal(decoded.Payload, payload) {
		t.Fatal("opaque payload did not round-trip")
	}
}

func TestTunnelCodecRejectsTransportTypeMismatch(t *testing.T) {
	codec := NewTunnelCodec(DefaultMaxFrameBytes)
	chunkJSON := []byte(`{"v":1,"type":"request_chunk","runnerId":"runner-1","endpointId":"endpoint-1","requestId":"request-1","seq":0,"timestamp":1}`)
	if _, err := codec.DecodeText(chunkJSON); !errors.Is(err, ErrFrameTypeMismatch) {
		t.Fatalf("text chunk error = %v", err)
	}

	control := validFrame(FrameHeartbeat)
	header, err := json.Marshal(control.commonHeader())
	if err != nil {
		t.Fatal(err)
	}
	binaryControl := make([]byte, 4+len(header))
	binary.BigEndian.PutUint32(binaryControl[:4], uint32(len(header)))
	copy(binaryControl[4:], header)
	if _, err := codec.DecodeBinary(binaryControl); !errors.Is(err, ErrFrameTypeMismatch) {
		t.Fatalf("binary control error = %v", err)
	}
}

func TestTunnelCodecRejectsUnsupportedVersionAndOversize(t *testing.T) {
	codec := NewTunnelCodec(128)
	unsupported := []byte(`{"v":2,"type":"heartbeat","runnerId":"runner-1","endpointId":"endpoint-1","seq":0,"timestamp":1}`)
	if _, err := codec.DecodeText(unsupported); !errors.Is(err, ErrFrameUnsupportedVersion) {
		t.Fatalf("unsupported version error = %v", err)
	}
	if _, err := codec.DecodeText(bytes.Repeat([]byte("x"), 129)); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("oversize text error = %v", err)
	}

	frame := validFrame(FrameResponseChunk)
	frame.Payload = bytes.Repeat([]byte("x"), 128)
	if _, err := codec.Encode(frame); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("oversize encode error = %v", err)
	}
}

func TestTunnelCodecRejectsInvalidBinaryHeaderLength(t *testing.T) {
	codec := NewTunnelCodec(DefaultMaxFrameBytes)
	for _, encoded := range [][]byte{
		{0, 0, 0},
		{0, 0, 0, 0},
		{0, 0, 0, 10, '{', '}'},
	} {
		if _, err := codec.DecodeBinary(encoded); !errors.Is(err, ErrFrameMalformed) {
			t.Fatalf("encoded length %d error = %v", len(encoded), err)
		}
	}
}
