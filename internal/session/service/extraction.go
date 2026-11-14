package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/markhuangai/dense-mem/internal/assessor"
	remember "github.com/markhuangai/dense-mem/internal/remember/service"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	session "github.com/markhuangai/dense-mem/internal/session/contract"
)

func (s *Service) extractWindows(ctx context.Context, submission *session.Submission) ([]session.ExtractionResponse, []string, error) {
	assessmentCtx, cancel := remember.ContextForPhase(ctx, remember.RememberPhaseAssessment)
	defer cancel()
	workerCtx, stop := context.WithCancel(assessmentCtx)
	defer stop()
	prior, warnings, err := s.boundedPriorContext(submission.Prior, submission.Intake.Tokenizer)
	if err != nil {
		return nil, warnings, err
	}
	windows := submission.Intake.Windows
	responses := make([]session.ExtractionResponse, len(windows))
	var group sync.WaitGroup
	var firstErr error
	var errorMu sync.Mutex
	semaphore := make(chan struct{}, 4)
	for index, window := range windows {
		group.Add(1)
		go func(index int, window session.Window) {
			defer group.Done()
			select {
			case semaphore <- struct{}{}:
			default:
				requestctx.ReportProgress(workerCtx, requestctx.ProgressUpdate{Stage: "session_window_wait"})
				select {
				case semaphore <- struct{}{}:
				case <-workerCtx.Done():
					return
				}
			}
			defer func() { <-semaphore }()
			request := session.ExtractionRequest{RequestID: fmt.Sprintf("session:%s:window:%d", submission.ID, window.Index), Window: window, Prior: prior}
			response, err := s.extractWindow(workerCtx, ctx, submission, request)
			if err != nil {
				errorMu.Lock()
				if firstErr == nil {
					firstErr = err
					stop()
				}
				errorMu.Unlock()
				return
			}
			responses[index] = response
		}(index, window)
	}
	group.Wait()
	if firstErr != nil {
		if err := assessmentCtx.Err(); err != nil {
			firstErr = errors.Join(err, firstErr)
		}
		return nil, warnings, firstErr
	}
	if err := workerCtx.Err(); err != nil {
		return nil, warnings, err
	}
	return responses, warnings, nil
}

func (s *Service) extractWindow(providerCtx, persistenceCtx context.Context, submission *session.Submission, request session.ExtractionRequest) (session.ExtractionResponse, error) {
	providerCtx = requestctx.WithProgressOperation(providerCtx, "session_extraction")
	var response session.ExtractionResponse
	var err error
	if cached := submission.Extractions[request.Window.Index]; len(cached) > 0 {
		requestctx.ReportProgress(providerCtx, requestctx.ProgressUpdate{Stage: "session_validation", Batch: request.Window.Index + 1, Batches: len(submission.Intake.Windows)})
		response, err = session.DecodeExtraction(cached)
		if err == nil {
			err = session.ValidateExtraction(request, response)
		}
	} else {
		requestctx.ReportProgress(providerCtx, requestctx.ProgressUpdate{Stage: "session_extraction", Batch: request.Window.Index + 1, Batches: len(submission.Intake.Windows)})
		response, err = s.deps.Extractor.Extract(providerCtx, request)
		if err == nil {
			requestctx.ReportProgress(providerCtx, requestctx.ProgressUpdate{Stage: "session_validation"})
			err = session.ValidateExtraction(request, response)
		}
		if err == nil {
			body, marshalErr := json.Marshal(response)
			if marshalErr != nil {
				err = marshalErr
			} else {
				requestctx.ReportProgress(persistenceCtx, requestctx.ProgressUpdate{Stage: "session_checkpoint", Batch: request.Window.Index + 1, Batches: len(submission.Intake.Windows)})
				err = s.deps.Repository.SaveSessionExtraction(persistenceCtx, submission.Intake.Scope, submission.ID, request.Window.Index, body)
			}
		}
	}
	if err != nil {
		return session.ExtractionResponse{}, err
	}
	if len(response.SecuritySignals) > 0 {
		return session.ExtractionResponse{}, session.ErrSecurity
	}
	return response, nil
}

func (s *Service) boundedPriorContext(events []session.PriorEvent, tokenizer string) ([]session.PriorEvent, []string, error) {
	selected := []session.PriorEvent{}
	if tokenizer == "" {
		tokenizer = "o200k_base"
	}
	for _, event := range events {
		candidate := append(append([]session.PriorEvent(nil), selected...), event)
		body, err := json.Marshal(candidate)
		if err != nil {
			return nil, nil, err
		}
		count, err := assessor.CountTokens(string(body), tokenizer)
		if err != nil {
			return nil, nil, err
		}
		if count > session.ContextTokens || len(candidate) > session.ContextEvents {
			continue
		}
		selected = candidate
	}
	for left, right := 0, len(selected)-1; left < right; left, right = left+1, right-1 {
		selected[left], selected[right] = selected[right], selected[left]
	}
	var warnings []string
	if len(selected) < len(events) {
		warnings = append(warnings, "Earlier session context was omitted to fit the bounded context budget.")
	}
	return selected, warnings, nil
}
