package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"testing"
)

func TestFullClientRequestEncodesConfig(t *testing.T) {
	frame, err := fullClientRequest(1, sessionPayload(16000))
	if err != nil {
		t.Fatalf("fullClientRequest: %v", err)
	}
	if got := frame[0]; got != 0x11 {
		t.Errorf("header[0] = %#x, want 0x11", got)
	}
	if got := frame[1] >> 4; got != msgClientFullRequest {
		t.Errorf("message type = %#x, want %#x", got, msgClientFullRequest)
	}
	if got := frame[1] & 0x0F; got != flagPosSequence {
		t.Errorf("flags = %#x, want %#x", got, flagPosSequence)
	}
	if got := frame[2] >> 4; got != serializationJSON {
		t.Errorf("serialization = %#x, want JSON", got)
	}
	if got := frame[2] & 0x0F; got != compressionGzip {
		t.Errorf("compression = %#x, want gzip", got)
	}

	if seq := int32(binary.BigEndian.Uint32(frame[4:8])); seq != 1 {
		t.Errorf("seq = %d, want 1", seq)
	}
	size := int(binary.BigEndian.Uint32(frame[8:12]))
	if size != len(frame)-12 {
		t.Errorf("declared size %d, actual %d", size, len(frame)-12)
	}
	body, err := gzipDecompress(frame[12:])
	if err != nil {
		t.Fatalf("gunzip body: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	req, _ := doc["request"].(map[string]any)
	if req["model_name"] != "bigmodel" {
		t.Errorf("model_name = %v, want bigmodel", req["model_name"])
	}
	if req["enable_nonstream"] != false {
		t.Errorf("enable_nonstream = %v, want false", req["enable_nonstream"])
	}
}

func TestAudioOnlyRequestLastFrameUsesNegativeSeq(t *testing.T) {
	pcm := []byte{1, 2, 3, 4, 5, 6}
	frame, err := audioOnlyRequest(7, pcm, true)
	if err != nil {
		t.Fatalf("audioOnlyRequest: %v", err)
	}
	if got := frame[1] >> 4; got != msgClientAudioOnly {
		t.Errorf("message type = %#x, want %#x", got, msgClientAudioOnly)
	}
	if got := frame[1] & 0x0F; got != flagNegWithSeq {
		t.Errorf("flags = %#x, want NEG_WITH_SEQUENCE", got)
	}
	if seq := int32(binary.BigEndian.Uint32(frame[4:8])); seq != -7 {
		t.Errorf("seq = %d, want -7", seq)
	}
	got, err := gzipDecompress(frame[12:])
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	if !bytes.Equal(got, pcm) {
		t.Errorf("payload = %v, want %v", got, pcm)
	}
}

func TestAudioOnlyRequestNonLastFrame(t *testing.T) {
	frame, err := audioOnlyRequest(3, []byte{9}, false)
	if err != nil {
		t.Fatalf("audioOnlyRequest: %v", err)
	}
	if got := frame[1] & 0x0F; got != flagPosSequence {
		t.Errorf("flags = %#x, want POS_SEQUENCE", got)
	}
	if seq := int32(binary.BigEndian.Uint32(frame[4:8])); seq != 3 {
		t.Errorf("seq = %d, want 3", seq)
	}
}

// serverFrame builds a synthetic server response frame for parsing tests.
func serverFrame(msgType, flags byte, seq *int32, code *int32, body []byte) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{
		(protocolVersionV1 << 4) | headerWords,
		(msgType << 4) | flags,
		(serializationJSON << 4) | compressionGzip,
		0x00,
	})
	if seq != nil {
		_ = binary.Write(&buf, binary.BigEndian, *seq)
	}
	if code != nil {
		_ = binary.Write(&buf, binary.BigEndian, *code)
	}
	compressed, _ := gzipCompress(body)
	_ = binary.Write(&buf, binary.BigEndian, uint32(len(compressed)))
	buf.Write(compressed)
	return buf.Bytes()
}

func TestParseServerFullResponse(t *testing.T) {
	seq := int32(42)
	body, _ := json.Marshal(map[string]any{
		"result": map[string]any{"text": "你好，世界。"},
	})
	// flags 0x03 = POS_SEQUENCE | is_last_package
	frame := serverFrame(msgServerFullResponse, 0x03, &seq, nil, body)

	resp, err := parseResponse(frame)
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
	}
	if !resp.Last {
		t.Error("Last = false, want true")
	}
	if resp.Seq != 42 {
		t.Errorf("Seq = %d, want 42", resp.Seq)
	}
	if resp.Text != "你好，世界。" {
		t.Errorf("Text = %q, want 你好，世界。", resp.Text)
	}
	if resp.Code != 0 {
		t.Errorf("Code = %d, want 0", resp.Code)
	}
}

func TestParseServerErrorResponse(t *testing.T) {
	seq := int32(1)
	code := int32(401)
	body, _ := json.Marshal(map[string]any{"message": "unauthorized"})
	frame := serverFrame(msgServerErrorResp, 0x01, &seq, &code, body)

	resp, err := parseResponse(frame)
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
	}
	if resp.Code != 401 {
		t.Errorf("Code = %d, want 401", resp.Code)
	}
	if resp.Raw["message"] != "unauthorized" {
		t.Errorf("Raw[message] = %v, want unauthorized", resp.Raw["message"])
	}
}

func TestParseResponseEmptyPayload(t *testing.T) {
	seq := int32(1)
	var buf bytes.Buffer
	buf.Write([]byte{
		(protocolVersionV1 << 4) | headerWords,
		(msgServerFullResponse << 4) | 0x01,
		(serializationJSON << 4) | compressionGzip,
		0x00,
	})
	_ = binary.Write(&buf, binary.BigEndian, seq)
	_ = binary.Write(&buf, binary.BigEndian, uint32(0)) // empty payload

	resp, err := parseResponse(buf.Bytes())
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
	}
	if resp.Text != "" || resp.Last {
		t.Errorf("unexpected response: %+v", resp)
	}
}
