package service

import (
	"strings"
	"testing"

	session "github.com/markhuangai/dense-mem/internal/session/contract"
	"github.com/stretchr/testify/require"
)

func sessionRequest(events ...session.Event) session.Request {
	return session.Request{IdempotencyKey: "retained-key", Framework: "generic", AppName: "notes", UserID: "external-user", SessionID: "external-session", Events: events}
}

func TestSessionWindowsPreserveLongUnicodeAndBoundaries(t *testing.T) {
	text := strings.Repeat("Ari writes Go. café 🧭\n", 2000)
	req := sessionRequest(session.Event{EventID: "long", Text: text})
	require.NoError(t, ValidateRequest(req))
	windows, err := BuildWindows(req, "o200k_base")
	require.NoError(t, err)
	require.Greater(t, len(windows), 1)
	var rebuilt strings.Builder
	end := 0
	for i, window := range windows {
		if i > 0 {
			require.Equal(t, windows[i-1].Core[len(windows[i-1].Core)-1], *window.Before)
		}
		if i+1 < len(windows) {
			require.Equal(t, windows[i+1].Core[0], *window.After)
		}
		for _, segment := range window.Core {
			require.Equal(t, end, segment.Start)
			require.Equal(t, string([]rune(text)[segment.Start:segment.End]), segment.Text)
			rebuilt.WriteString(segment.Text)
			end = segment.End
		}
	}
	require.Equal(t, text, rebuilt.String())
}

func TestSessionRepeatedEventsAndTimestampConflicts(t *testing.T) {
	event := session.Event{EventID: "one", Text: "Ari uses Go."}
	req := sessionRequest(event, event)
	require.NoError(t, ValidateRequest(req))
	windows, err := BuildWindows(req, "o200k_base")
	require.NoError(t, err)
	require.Len(t, windows, 1)
	require.Len(t, windows[0].Core, 1)
	changed := "2026-10-10T12:00:00Z"
	req.Events[1].OccurredAt = &changed
	require.ErrorIs(t, ValidateRequest(req), session.ErrEventConflict)
}

func TestSessionWholeRequestHashIncludesOrderAndProvenance(t *testing.T) {
	req := sessionRequest(session.Event{EventID: "one", Text: "Ari uses Go."}, session.Event{EventID: "two", Text: "Ari uses Rust."})
	original, err := RequestHash(req)
	require.NoError(t, err)
	req.Events[0], req.Events[1] = req.Events[1], req.Events[0]
	reordered, err := RequestHash(req)
	require.NoError(t, err)
	require.NotEqual(t, original, reordered)
	req.AppName = "another-app"
	changed, err := RequestHash(req)
	require.NoError(t, err)
	require.NotEqual(t, changed, reordered)
}

func TestSessionAdmissionRejectsOversizedTextWithoutTruncation(t *testing.T) {
	req := sessionRequest(session.Event{EventID: "huge", Text: strings.Repeat("🧭", 100000)})
	windows, err := BuildWindows(req, "o200k_base")
	require.ErrorIs(t, err, session.ErrBudget)
	require.Nil(t, windows)
}
