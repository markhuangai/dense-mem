package handler

import (
	"bufio"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/modelprovider"
	remember "github.com/markhuangai/dense-mem/internal/remember/service"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/stretchr/testify/require"
)

func TestMCPHandlerRememberProgressReportsActualCapacityWaitAndRepeatedStages(t *testing.T) {
	gate := modelprovider.NewConcurrencyGate(1)
	require.NoError(t, modelprovider.AcquireConcurrency(t.Context(), gate))
	released := false
	defer func() {
		if !released {
			modelprovider.ReleaseConcurrency(gate)
		}
	}()
	server := newProgressHTTPServer(t, nil, func(ctx context.Context, _ string, _ map[string]any) (map[string]any, error) {
		ctx = requestctx.WithProgressOperation(ctx, "partition")
		if err := modelprovider.AcquireConcurrency(ctx, gate); err != nil {
			return nil, err
		}
		defer modelprovider.ReleaseConcurrency(gate)
		remember.ReportProgress(ctx, remember.ProgressValidatingPartitions)
		requestctx.ReportProgress(ctx, requestctx.ProgressUpdate{Stage: string(remember.ProgressRegeneratingPartitions), Attempt: 2, Total: 3})
		remember.ReportProgress(ctx, remember.ProgressValidatingPartitions)
		remember.ReportProgress(ctx, remember.ProgressCommitting)
		return map[string]any{"completed": true}, nil
	})
	response := progressHTTPCall(t, server.URL, "2026-07-28", "text/event-stream", "9007199254740993", "remember", "1")
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	var first strings.Builder
	for {
		line, err := reader.ReadString('\n')
		require.NoError(t, err)
		first.WriteString(line)
		if line == "\n" {
			break
		}
	}
	require.Contains(t, first.String(), "Waiting for partition provider capacity")
	require.Contains(t, first.String(), "9007199254740993")
	require.NotContains(t, first.String(), "completed")
	modelprovider.ReleaseConcurrency(gate)
	released = true
	rest, err := io.ReadAll(reader)
	require.NoError(t, err)
	frames := decodeProgressFrames(t, strings.NewReader(first.String()+string(rest)))
	var messages []string
	for i, frame := range frames[:len(frames)-1] {
		require.Equal(t, i+1, frame.Params.Progress)
		messages = append(messages, frame.Params.Message)
	}
	require.Equal(t, []string{"Waiting for partition provider capacity", "Calling partition provider", "Validating partitions", "Regenerating partitions (2/3)", "Validating partitions", "Committing"}, messages)
	require.Contains(t, string(frames[len(frames)-1].Result), "completed")
}

func TestMCPHandlerRememberProgressPrecedesBlockedStages(t *testing.T) {
	for _, test := range []struct {
		stage   remember.ProgressStage
		message string
	}{
		{remember.ProgressPartitioning, "Partitioning submitted text"},
		{remember.ProgressAssessingEvidence, "Assessing Evidence"},
		{remember.ProgressDuplicateEmbeddings, "Preparing duplicate-search embeddings"},
		{remember.ProgressPreparingSearch, "Preparing search state"},
		{remember.ProgressCommitting, "Committing"},
	} {
		t.Run(string(test.stage), func(t *testing.T) {
			release := make(chan struct{})
			finished := make(chan struct{})
			defer close(release)
			server := newProgressHTTPServer(t, nil, func(ctx context.Context, _ string, _ map[string]any) (map[string]any, error) {
				remember.ReportProgress(ctx, test.stage)
				select {
				case <-release:
				case <-ctx.Done():
				}
				close(finished)
				return map[string]any{"completed": true}, nil
			})
			response := progressHTTPCall(t, server.URL, "2026-07-28", "text/event-stream", `"blocked-stage"`, "remember", "1")
			defer response.Body.Close()
			first := make(chan string, 1)
			go func() {
				reader := bufio.NewReader(response.Body)
				var event strings.Builder
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						first <- err.Error()
						return
					}
					event.WriteString(line)
					if line == "\n" {
						first <- event.String()
						return
					}
				}
			}()
			select {
			case event := <-first:
				require.Contains(t, event, test.message)
				require.NotContains(t, event, "completed")
			case <-time.After(5 * time.Second):
				t.Fatal("progress did not arrive during blocked work")
			}
			select {
			case <-finished:
				t.Fatal("blocked work completed before progress was observed")
			default:
			}
		})
	}
}
