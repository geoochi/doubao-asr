package main

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
)

// Implementation of the Doubao (Volcengine) streaming-ASR websocket protocol.
//
// Every frame begins with a 4-byte header:
//
//	byte 0: (version<<4) | headerSizeInWords
//	byte 1: (messageType<<4) | messageTypeSpecificFlags
//	byte 2: (serialization<<4) | compression
//	byte 3: reserved (0)
//
// Request payloads are gzip-compressed JSON (or raw PCM for audio frames) and
// are preceded by a big-endian int32 sequence and a big-endian uint32 size.
const (
	protocolVersionV1 = 0b0001

	msgClientFullRequest  = 0b0001
	msgClientAudioOnly    = 0b0010
	msgServerFullResponse = 0b1001
	msgServerErrorResp    = 0b1111

	flagPosSequence = 0b0001
	flagNegWithSeq  = 0b0011

	// The reference implementation also labels audio-only frames as JSON.
	serializationJSON = 0b0001

	compressionGzip = 0b0001

	headerWords = 1 // header size in 4-byte words
)

// header builds the 4-byte frame header.
func header(msgType, flags byte) []byte {
	return []byte{
		(protocolVersionV1 << 4) | headerWords,
		(msgType << 4) | flags,
		(serializationJSON << 4) | compressionGzip,
		0x00,
	}
}

// fullClientRequest builds the first frame, carrying the session configuration.
func fullClientRequest(seq int32, payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}
	compressed, err := gzipCompress(body)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	buf.Write(header(msgClientFullRequest, flagPosSequence))
	_ = binary.Write(&buf, binary.BigEndian, seq)
	_ = binary.Write(&buf, binary.BigEndian, uint32(len(compressed)))
	buf.Write(compressed)
	return buf.Bytes(), nil
}

// audioOnlyRequest builds an audio frame. The final frame is marked with a
// negative sequence and the NEG_WITH_SEQUENCE flag to signal end of stream.
func audioOnlyRequest(seq int32, pcm []byte, last bool) ([]byte, error) {
	flags := byte(flagPosSequence)
	if last {
		flags = flagNegWithSeq
		seq = -seq
	}
	compressed, err := gzipCompress(pcm)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	buf.Write(header(msgClientAudioOnly, flags))
	_ = binary.Write(&buf, binary.BigEndian, seq)
	_ = binary.Write(&buf, binary.BigEndian, uint32(len(compressed)))
	buf.Write(compressed)
	return buf.Bytes(), nil
}

// asrResponse is the decoded form of a server frame we care about.
type asrResponse struct {
	Code  int32
	Event int32
	Last  bool
	Seq   int32
	Text  string
	Raw   map[string]any
}

// parseResponse decodes a server frame.
func parseResponse(msg []byte) (*asrResponse, error) {
	if len(msg) < 4 {
		return nil, fmt.Errorf("response too short (%d bytes)", len(msg))
	}
	headerLen := int(msg[0]&0x0F) * 4
	msgType := msg[1] >> 4
	flags := msg[1] & 0x0F
	serialization := msg[2] >> 4
	compression := msg[2] & 0x0F

	if headerLen > len(msg) {
		return nil, fmt.Errorf("bad header size %d", headerLen)
	}
	payload := msg[headerLen:]
	resp := &asrResponse{}

	if flags&0x01 != 0 {
		if len(payload) < 4 {
			return nil, io.ErrUnexpectedEOF
		}
		resp.Seq = int32(binary.BigEndian.Uint32(payload[:4]))
		payload = payload[4:]
	}
	if flags&0x02 != 0 {
		resp.Last = true
	}
	if flags&0x04 != 0 {
		if len(payload) < 4 {
			return nil, io.ErrUnexpectedEOF
		}
		resp.Event = int32(binary.BigEndian.Uint32(payload[:4]))
		payload = payload[4:]
	}

	switch msgType {
	case msgServerFullResponse:
		if len(payload) < 4 {
			return nil, io.ErrUnexpectedEOF
		}
		payload = payload[4:] // skip payload size
	case msgServerErrorResp:
		if len(payload) < 8 {
			return nil, io.ErrUnexpectedEOF
		}
		resp.Code = int32(binary.BigEndian.Uint32(payload[:4]))
		payload = payload[8:] // skip code + payload size
	}

	if len(payload) == 0 {
		return resp, nil
	}
	if compression == compressionGzip {
		out, err := gzipDecompress(payload)
		if err != nil {
			return resp, fmt.Errorf("gunzip response: %w", err)
		}
		payload = out
	}
	if serialization == serializationJSON {
		var doc struct {
			Result struct {
				Text string `json:"text"`
			} `json:"result"`
		}
		if err := json.Unmarshal(payload, &doc); err == nil {
			resp.Text = doc.Result.Text
		}
		var raw map[string]any
		if err := json.Unmarshal(payload, &raw); err == nil {
			resp.Raw = raw
		}
	}
	return resp, nil
}

// authHeaders builds the headers required by the new-console (X-Api-Key) auth.
func authHeaders(resourceID, apiKey string) map[string]string {
	return map[string]string{
		"X-Api-Resource-Id": resourceID,
		"X-Api-Request-Id":  newRequestID(),
		"X-Api-Key":         apiKey,
	}
}

// sessionPayload is the JSON body of the initial full-client request.
func sessionPayload(sampleRate int) map[string]any {
	return map[string]any{
		"user": map[string]any{"uid": "doubao-dictate"},
		"audio": map[string]any{
			"format":  "pcm",
			"codec":   "raw",
			"rate":    sampleRate,
			"bits":    16,
			"channel": 1,
		},
		"request": map[string]any{
			"model_name":       "bigmodel",
			"enable_itn":       true,
			"enable_punc":      true,
			"enable_ddc":       true,
			"show_utterances":  true,
			"enable_nonstream": false,
		},
	}
}

func gzipCompress(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func gzipDecompress(data []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// newRequestID returns a random UUIDv4 string for the X-Api-Request-Id header.
func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	dst := make([]byte, 36)
	hex.Encode(dst[0:8], b[0:4])
	dst[8] = '-'
	hex.Encode(dst[9:13], b[4:6])
	dst[13] = '-'
	hex.Encode(dst[14:18], b[6:8])
	dst[18] = '-'
	hex.Encode(dst[19:23], b[8:10])
	dst[23] = '-'
	hex.Encode(dst[24:36], b[10:16])
	return string(dst)
}
