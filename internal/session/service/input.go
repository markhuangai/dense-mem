package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/markhuangai/dense-mem/internal/assessor"
	session "github.com/markhuangai/dense-mem/internal/session/contract"
)

func ValidateRequest(req session.Request) error {
	for key, value := range map[string]string{
		"idempotency_key": req.IdempotencyKey, "framework": req.Framework,
		"app_name": req.AppName, "user_id": req.UserID, "session_id": req.SessionID,
	} {
		limit := 256
		if key == "idempotency_key" {
			limit = 128
		}
		if !utf8.ValidString(value) || strings.TrimSpace(value) == "" || utf8.RuneCountInString(value) > limit {
			return fmt.Errorf("%w: %s must contain 1–%d characters", session.ErrInvalidInput, key, limit)
		}
	}
	if len(req.Events) < 1 || len(req.Events) > session.MaxEvents {
		return fmt.Errorf("%w: events must contain 1–%d entries", session.ErrInvalidInput, session.MaxEvents)
	}
	seen := map[string]session.Event{}
	for i, event := range req.Events {
		if !utf8.ValidString(event.EventID) || strings.TrimSpace(event.EventID) == "" || utf8.RuneCountInString(event.EventID) > 256 {
			return fmt.Errorf("%w: events[%d].event_id must contain 1–256 characters", session.ErrInvalidInput, i)
		}
		if !utf8.ValidString(event.Text) || strings.TrimSpace(event.Text) == "" {
			return fmt.Errorf("%w: events[%d].text must contain exact nonempty Unicode text", session.ErrInvalidInput, i)
		}
		if event.OccurredAt != nil {
			if _, err := time.Parse(time.RFC3339Nano, *event.OccurredAt); err != nil {
				return fmt.Errorf("%w: events[%d].occurred_at must be RFC3339", session.ErrInvalidInput, i)
			}
		}
		if previous, ok := seen[event.EventID]; ok && !session.SameEvent(previous, event) {
			return session.ErrEventConflict
		}
		seen[event.EventID] = event
	}
	return nil
}

func RequestHash(req session.Request) (string, error) {
	encoded, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func BuildWindows(req session.Request, tokenizer string) ([]session.Window, error) {
	segments := []session.Segment{}
	seen := map[string]bool{}
	for index, event := range req.Events {
		if seen[event.EventID] {
			continue
		}
		seen[event.EventID] = true
		runes := []rune(event.Text)
		for start := 0; start < len(runes); {
			end := min(start+session.SegmentRunes, len(runes))
			if end < len(runes) {
				for candidate := end - 1; candidate >= max(start+1, end-128); candidate-- {
					if runes[candidate] == '\n' {
						end = candidate + 1
						break
					}
				}
			}
			segments = append(segments, session.Segment{
				Ref: fmt.Sprintf("event:%d:%d", index, start), EventIndex: index,
				Start: start, End: end, Text: string(runes[start:end]),
			})
			start = end
		}
	}
	if tokenizer == "" {
		tokenizer = "o200k_base"
	}
	windows := []session.Window{}
	for start := 0; start < len(segments); {
		end, tokens := start, 0
		for end < len(segments) {
			count, err := assessor.CountTokens(segments[end].Text, tokenizer)
			if err != nil {
				return nil, err
			}
			if count > session.WindowTokens {
				return nil, session.ErrBudget
			}
			if tokens+count > session.WindowTokens {
				break
			}
			tokens += count
			end++
		}
		if len(windows) == session.MaxWindows {
			return nil, session.ErrBudget
		}
		window := session.Window{Index: len(windows), Core: append([]session.Segment(nil), segments[start:end]...)}
		if start > 0 {
			before := segments[start-1]
			window.Before = &before
		}
		if end < len(segments) {
			after := segments[end]
			window.After = &after
		}
		windows = append(windows, window)
		start = end
	}
	return windows, nil
}
