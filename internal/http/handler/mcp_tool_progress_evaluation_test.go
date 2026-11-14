//go:build evaluation

package handler

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/stretchr/testify/require"
)

func TestMCPHandlerEvaluationToolProgress(t *testing.T) {
	for _, name := range []string{"eval_list_knowledge_refs", "eval_run_recall_case"} {
		t.Run(name, func(t *testing.T) {
			server := newProgressHTTPServer(t, nil, func(ctx context.Context, _ string, _ map[string]any) (map[string]any, error) {
				requestctx.ReportProgress(ctx, requestctx.ProgressUpdate{Stage: "evaluation_audit"})
				stage := "evaluation_list"
				if name == "eval_run_recall_case" {
					stage = "recall_policy"
				}
				requestctx.ReportProgress(ctx, requestctx.ProgressUpdate{Stage: stage})
				requestctx.ReportProgress(ctx, requestctx.ProgressUpdate{Stage: "preparing_result"})
				requestctx.ReportProgress(ctx, requestctx.ProgressUpdate{Stage: "untrusted private input"})
				return map[string]any{"completed": true}, nil
			})
			for _, version := range []string{"2025-11-25", "2026-07-28"} {
				response := progressHTTPCall(t, server.URL, version, "text/event-stream", "9007199254740993", name, "1")
				frames := decodeProgressFrames(t, response.Body)
				require.NoError(t, response.Body.Close())
				require.Len(t, frames, 4)
				for index, frame := range frames[:3] {
					require.Equal(t, "9007199254740993", string(frame.Params.Token))
					require.Equal(t, index+1, frame.Params.Progress)
					require.NotContains(t, frame.Params.Message, "private")
				}
				require.Equal(t, "Recording evaluation audit", frames[0].Params.Message)
				require.Equal(t, "Preparing result", frames[2].Params.Message)
				require.Contains(t, string(frames[3].Result), "completed")
				terminal := progressHTTPCall(t, server.URL, version, "application/json", "0", name, "1")
				var frame progressFrame
				require.NoError(t, json.NewDecoder(terminal.Body).Decode(&frame))
				require.NoError(t, terminal.Body.Close())
				require.JSONEq(t, string(frames[3].Result), string(frame.Result))
			}
		})
	}
}
