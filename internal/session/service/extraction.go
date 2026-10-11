package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/markhuangai/dense-mem/internal/assessor"
	remember "github.com/markhuangai/dense-mem/internal/remember/service"
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
			case <-workerCtx.Done():
				return
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
	var response session.ExtractionResponse
	var err error
	if cached := submission.Extractions[request.Window.Index]; len(cached) > 0 {
		response, err = session.DecodeExtraction(cached)
		if err == nil {
			err = session.ValidateExtraction(request, response)
		}
	} else {
		response, err = s.deps.Extractor.Extract(providerCtx, request)
		if err == nil {
			err = session.ValidateExtraction(request, response)
		}
		if err == nil {
			body, marshalErr := json.Marshal(response)
			if marshalErr != nil {
				err = marshalErr
			} else {
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
	warnings := []string{}
	if len(selected) < len(events) {
		warnings = append(warnings, "Earlier session context was omitted to fit the bounded context budget.")
	}
	return selected, warnings, nil
}

func (s *Service) linkEntities(ctx context.Context, submission *session.Submission, request session.LinkingRequest) (session.LinkingResponse, error) {
	var response session.LinkingResponse
	var err error
	if len(submission.Linked) > 0 {
		response, err = session.DecodeLinking(submission.Linked)
		if err == nil {
			err = session.ValidateLinking(request, response)
		}
		return response, err
	}
	assessmentCtx, cancel := remember.ContextForPhase(ctx, remember.RememberPhaseAssessment)
	response, err = s.deps.Extractor.Link(assessmentCtx, request)
	if err != nil && assessmentCtx.Err() != nil {
		err = errors.Join(assessmentCtx.Err(), err)
	}
	cancel()
	if err != nil {
		return session.LinkingResponse{}, err
	}
	if err := session.ValidateLinking(request, response); err != nil {
		return session.LinkingResponse{}, err
	}
	body, err := json.Marshal(response)
	if err != nil {
		return session.LinkingResponse{}, err
	}
	if err := s.deps.Repository.SaveSessionLinking(ctx, submission.Intake.Scope, submission.ID, body); err != nil {
		return session.LinkingResponse{}, err
	}
	return response, nil
}

func buildLinkingRequest(submission *session.Submission, responses []session.ExtractionResponse) (session.LinkingRequest, error) {
	request := session.LinkingRequest{RequestID: "session:" + submission.ID + ":linking", Entities: []session.EntityProposal{}, Relationships: []session.RelationshipProposal{}, Segments: []session.Segment{}}
	seenSegments := map[string]bool{}
	for index, response := range responses {
		window := submission.Intake.Windows[index]
		prefix := fmt.Sprintf("w%d:", window.Index)
		for _, entity := range response.Entities {
			entity.Ref = prefix + entity.Ref
			request.Entities = append(request.Entities, entity)
		}
		cited := map[string]bool{}
		for _, relationship := range response.Relationships {
			relationship.Ref = prefix + relationship.Ref
			relationship.SubjectRef = prefix + relationship.SubjectRef
			if relationship.ObjectRef != nil {
				qualified := prefix + *relationship.ObjectRef
				relationship.ObjectRef = &qualified
			}
			request.Relationships = append(request.Relationships, relationship)
			for _, citation := range relationship.Citations {
				cited[citation.StartRef] = true
				cited[citation.EndRef] = true
			}
		}
		for _, segment := range session.WindowSegments(window) {
			if !cited[segment.Ref] || seenSegments[segment.Ref] {
				continue
			}
			seenSegments[segment.Ref] = true
			request.Segments = append(request.Segments, segment)
		}
	}
	if len(request.Entities) > 3200 || len(request.Relationships) > 1600 {
		return session.LinkingRequest{}, session.ErrBudget
	}
	return request, nil
}
