package handler

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/require"
)

func readProgressFrames(body io.Reader) ([]progressFrame, error) {
	var frames []progressFrame
	var data strings.Builder
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" && data.Len() > 0 {
			var frame progressFrame
			if err := json.Unmarshal([]byte(data.String()), &frame); err != nil {
				return frames, err
			}
			frames = append(frames, frame)
			data.Reset()
		} else if strings.HasPrefix(line, "data:") {
			value := strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")
			if data.Len()+len(value)+1 > bufio.MaxScanTokenSize {
				return frames, errors.New("progress event exceeds its bounded capture")
			}
			data.WriteString(value)
			data.WriteByte('\n')
		}
	}
	if err := scanner.Err(); err != nil {
		return frames, err
	}
	if data.Len() > 0 {
		return frames, errors.New("progress stream ended with an incomplete event")
	}
	return frames, nil
}

func TestProgressStreamReader(t *testing.T) {
	const progress = "event: message\ndata: {\"method\":\"notifications/progress\",\"params\":{\"message\":\"Evidence é 🧭\"}}\n\n"
	const terminal = `data: {"id":1,"result":{}}`
	for _, test := range []struct {
		name, input string
		count       int
		fails       bool
	}{
		{"LF", progress + terminal + "\n\n", 2, false},
		{"CRLF", strings.ReplaceAll(progress+terminal+"\n\n", "\n", "\r\n"), 2, false},
		{"multiline data", "data:{\n: comment\ndata:\"id\":1,\"result\":{}}\n\n", 1, false},
		{"partial JSON", `data: {"id":1`, 0, true},
		{"JSON without newline", terminal, 0, true},
		{"JSON with one newline", terminal + "\n", 0, true},
		{"JSON with one CRLF", terminal + "\r\n", 0, true},
		{"prior complete frame", progress + terminal + "\n", 1, true},
		{"malformed complete event", progress + "data: {\n\n", 1, true},
		{"line limit", "data: " + strings.Repeat(" ", bufio.MaxScanTokenSize) + "\n\n", 0, true},
		{"event limit", strings.Repeat("data: "+strings.Repeat(" ", bufio.MaxScanTokenSize/2)+"\n", 2) + "\n", 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			frames, err := readProgressFrames(iotest.OneByteReader(strings.NewReader(test.input)))
			if test.fails {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Len(t, frames, test.count)
			if strings.HasPrefix(test.input, "event:") {
				require.Equal(t, "Evidence é 🧭", frames[0].Params.Message)
				if len(frames) == 2 {
					require.JSONEq(t, "1", string(frames[1].ID))
				}
			}
		})
	}
}
