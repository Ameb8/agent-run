package execution

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
)

var ErrJSONLFrameTooLarge = errors.New("RPC frame exceeds limit")

// MaxRPCFrameBytes bounds untrusted Pi control-plane frames independently of
// provider payloads. JSONL is strict: CRLF and unterminated final frames are
// rejected so a truncated child stream cannot be mistaken for a response.
const MaxRPCFrameBytes = 4 << 20

type JSONL struct {
	r *bufio.Reader
	w *bufio.Writer
}

func NewJSONL(reader io.Reader, writer io.Writer) *JSONL {
	return &JSONL{r: bufio.NewReaderSize(reader, MaxRPCFrameBytes+1), w: bufio.NewWriterSize(writer, MaxRPCFrameBytes+1)}
}

func (j *JSONL) Read(value any) error {
	if j == nil || j.r == nil {
		return errors.New("RPC reader unavailable")
	}
	line, err := j.r.ReadBytes('\n')
	if errors.Is(err, bufio.ErrBufferFull) || len(line) > MaxRPCFrameBytes {
		return ErrJSONLFrameTooLarge
	}
	if err != nil {
		return errors.New("truncated RPC frame")
	}
	if len(line) < 2 || line[len(line)-2] == '\r' {
		return errors.New("invalid RPC frame")
	}
	if err := json.Unmarshal(line[:len(line)-1], value); err != nil {
		return errors.New("invalid RPC frame")
	}
	return nil
}

func (j *JSONL) Write(value any) error {
	if j == nil || j.w == nil {
		return errors.New("RPC writer unavailable")
	}
	encoded, err := json.Marshal(value)
	if len(encoded) > MaxRPCFrameBytes {
		return ErrJSONLFrameTooLarge
	}
	if err != nil {
		return errors.New("invalid RPC frame")
	}
	if _, err = j.w.Write(encoded); err != nil {
		return err
	}
	if err = j.w.WriteByte('\n'); err != nil {
		return err
	}
	return j.w.Flush()
}
