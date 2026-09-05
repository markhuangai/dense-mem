package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	evidenceDiscoveryLockNamespace               = "dense-mem:dream-evidence:v1:"
	evidenceDiscoveryLockHashSeed          int64 = 323
	evidenceDiscoveryLockPollInterval            = 25 * time.Millisecond
	evidenceDiscoveryLockCleanupTimeout          = 5 * time.Second
	evidenceDiscoveryAttemptReservationTTL       = 6 * time.Hour
)

var ErrEvidenceDiscoveryBusy = errors.New("evidence discovery target lock is busy")
var ErrEvidenceDiscoveryAttemptNotReserved = errors.New("evidence discovery attempt is not reserved")

// WithEvidenceDiscoveryTargetLock serializes one target/content version across
// repository instances while the provider evaluates it. The callback receives
// a durable reservation, or a zero-valued attempt when the target is closed.
func (r *SemanticRepositoryImpl) WithEvidenceDiscoveryTargetLock(
	ctx context.Context,
	teamID string,
	targetEvidenceID string,
	contentHash string,
	fn func(EvidenceDiscoveryAttempt) error,
) error {
	if r == nil || r.db == nil {
		return errors.New("evidence discovery target lock: database is required")
	}
	if r.rls == nil {
		return errors.New("evidence discovery target lock: rls helper is required")
	}
	teamID = strings.TrimSpace(teamID)
	targetEvidenceID = strings.TrimSpace(targetEvidenceID)
	contentHash = strings.TrimSpace(contentHash)
	if _, err := uuid.Parse(teamID); err != nil {
		return fmt.Errorf("evidence discovery target lock: team_id is required: %w", err)
	}
	if _, err := uuid.Parse(targetEvidenceID); err != nil {
		return fmt.Errorf("evidence discovery target lock: target_evidence_id is required: %w", err)
	}
	if contentHash == "" {
		return errors.New("evidence discovery target lock: content_hash is required")
	}
	if fn == nil {
		return errors.New("evidence discovery target lock: callback is required")
	}
	releaseAdmission, err := acquireSharedAdvisoryLockAdmission(ctx, r.db, sharedAdvisoryLockAdmissionLimit)
	if err != nil {
		if errors.Is(err, errAdvisoryLockAdmissionBusy) {
			return ErrEvidenceDiscoveryBusy
		}
		return err
	}
	defer releaseAdmission()
	appDB, err := r.db.DB()
	if err != nil {
		return fmt.Errorf("evidence discovery target lock: application database handle: %w", err)
	}
	lockConn, err := appDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("evidence discovery target lock: acquire application connection: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = lockConn.Close()
		}
	}()
	key := evidenceDiscoveryLockNamespace + teamID + ":" + targetEvidenceID + ":" + contentHash
	_, _, err = acquireSessionAdvisoryLock(ctx, lockConn, key, evidenceDiscoveryLockHashSeed, evidenceDiscoveryLockPollInterval)
	if err != nil {
		_ = discardAdvisoryLockConnection(lockConn)
		closed = true
		return fmt.Errorf("evidence discovery target lock acquire: %w", err)
	}

	attempt, err := r.reserveEvidenceDiscoveryAttempt(ctx, teamID, targetEvidenceID, contentHash)
	if err != nil {
		cleanupErr := discardAdvisoryLockConnection(lockConn)
		closed = true
		return errors.Join(err, cleanupErr)
	}
	callbackReturned := false
	defer func() {
		if callbackReturned {
			return
		}
		panicValue := recover()
		cleanupErr := discardAdvisoryLockConnection(lockConn)
		closed = true
		if cleanupErr != nil {
			panic(errors.Join(fmt.Errorf("evidence discovery callback panicked: %v", panicValue), cleanupErr))
		}
		panic(panicValue)
	}()
	callbackErr := fn(attempt)
	callbackReturned = true
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), evidenceDiscoveryLockCleanupTimeout)
	var released bool
	releaseErr := lockConn.QueryRowContext(releaseCtx,
		"SELECT pg_advisory_unlock(hashtextextended($1, $2))", key, evidenceDiscoveryLockHashSeed,
	).Scan(&released)
	cancel()
	if releaseErr == nil && !released {
		releaseErr = errors.New("database did not release the evidence discovery advisory lock")
	}
	var closeErr error
	if releaseErr != nil {
		closeErr = discardAdvisoryLockConnection(lockConn)
		closed = true
	} else {
		closeErr = lockConn.Close()
		closed = true
	}
	if releaseErr != nil {
		releaseErr = fmt.Errorf("evidence discovery target lock release: %w", releaseErr)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("evidence discovery target lock connection close: %w", closeErr)
	}
	return errors.Join(callbackErr, releaseErr, closeErr)
}

func acquireSessionAdvisoryLock(
	ctx context.Context,
	conn *sql.Conn,
	key string,
	seed int64,
	pollInterval time.Duration,
) (bool, bool, error) {
	waited := false
	for {
		var acquired bool
		if err := conn.QueryRowContext(ctx,
			"SELECT pg_try_advisory_lock(hashtextextended($1, $2))", key, seed,
		).Scan(&acquired); err != nil {
			return false, waited, err
		}
		if acquired {
			return true, waited, nil
		}
		waited = true
		timer := time.NewTimer(pollInterval)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return false, waited, ctx.Err()
		}
	}
}

type evidenceDiscoveryAttemptState struct {
	attemptID           string
	reservationToken    string
	passNumber          int
	status              string
	dispatchStarted     bool
	acceptedProposals   int
	createdHypotheses   int
	evaluationPersisted bool
}

func (r *SemanticRepositoryImpl) reserveEvidenceDiscoveryAttempt(
	ctx context.Context,
	teamID string,
	targetEvidenceID string,
	contentHash string,
) (EvidenceDiscoveryAttempt, error) {
	var attempt EvidenceDiscoveryAttempt
	err := r.withDreamWriteTx(ctx, teamID, "", true, func(tx *gorm.DB) error {
		// A reservation that expired without reaching validated response is safe
		// to reclaim. Validated attempts are never rewritten or reclaimed.
		if err := tx.WithContext(ctx).Exec(`
			UPDATE dream_evidence_target_attempts
			SET status = 'abandoned', abandoned_at = now(), reservation_expires_at = now(), updated_at = now()
			WHERE team_id = ?::uuid
			  AND target_evidence_id = ?::uuid
			  AND target_content_hash = ?
			  AND status = 'reserved'
			  AND dispatch_started_at IS NULL
			  AND reservation_expires_at <= now()
		`, teamID, targetEvidenceID, contentHash).Error; err != nil {
			return err
		}
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT attempt_id::text, reservation_token::text, pass_number, status,
			       dispatch_started_at IS NOT NULL,
			       accepted_proposals, created_hypotheses, evaluation_persisted
			FROM dream_evidence_target_attempts
			WHERE team_id = ?::uuid
			  AND target_evidence_id = ?::uuid
			  AND target_content_hash = ?
			ORDER BY pass_number
			FOR UPDATE
		`, teamID, targetEvidenceID, contentHash).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		states := make([]evidenceDiscoveryAttemptState, 0, 2)
		for rows.Next() {
			var state evidenceDiscoveryAttemptState
			if err := rows.Scan(&state.attemptID, &state.reservationToken, &state.passNumber, &state.status,
				&state.dispatchStarted,
				&state.acceptedProposals, &state.createdHypotheses, &state.evaluationPersisted); err != nil {
				return err
			}
			states = append(states, state)
		}
		if err := rows.Err(); err != nil {
			return err
		}

		candidatePass := 1
		for _, state := range states {
			if state.status == "reserved" {
				// A reserved row with a started dispatch is never reclaimed after an
				// uncertain provider outcome; the next worker observes a closed target.
				return nil
			}
			if state.status != "validated" {
				continue
			}
			switch state.passNumber {
			case 1:
				if state.evaluationPersisted && state.createdHypotheses == 0 {
					return nil
				}
				if !state.evaluationPersisted && state.acceptedProposals == 0 {
					return nil
				}
				candidatePass = 2
			case 2:
				return nil
			}
		}
		if candidatePass > 2 {
			return nil
		}

		reservationToken := uuid.NewString()
		expiresAt := time.Now().UTC().Add(evidenceDiscoveryAttemptReservationTTL)
		// Reuse only a prior unvalidated/abandoned reservation for this exact
		// pass. The unique target/pass key prevents a second reservation.
		var reused bool
		if err := tx.WithContext(ctx).Raw(`
			UPDATE dream_evidence_target_attempts
			SET reservation_token = ?::uuid, status = 'reserved', reserved_at = now(),
			    reservation_expires_at = ?, abandoned_at = NULL, validated_at = NULL,
			    dispatch_started_at = NULL,
			    accepted_proposals = 0, created_hypotheses = 0, evaluation_persisted = false,
			    updated_at = now()
			WHERE team_id = ?::uuid
			  AND target_evidence_id = ?::uuid
			  AND target_content_hash = ?
			  AND pass_number = ?
				AND status = 'abandoned'
			RETURNING attempt_id::text, reservation_token::text, pass_number
		`, reservationToken, expiresAt, teamID, targetEvidenceID, contentHash, candidatePass).Row().Scan(
			&attempt.AttemptID, &attempt.ReservationToken, &attempt.PassNumber,
		); err == nil {
			reused = true
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if reused {
			return nil
		}
		return tx.WithContext(ctx).Raw(`
			INSERT INTO dream_evidence_target_attempts (
			    team_id, attempt_id, target_evidence_id, target_content_hash,
			    space_id, space_generation, pass_number, reservation_token,
			    status, reserved_at, reservation_expires_at
			) VALUES (?, gen_random_uuid(), ?::uuid, ?, dense_mem_team_shared_space(?::uuid),
			          dense_mem_team_shared_generation(?::uuid), ?, ?::uuid, 'reserved', now(), ?)
			RETURNING attempt_id::text, reservation_token::text, pass_number
		`, teamID, targetEvidenceID, contentHash, teamID, teamID, candidatePass,
			reservationToken, expiresAt).Row().Scan(&attempt.AttemptID, &attempt.ReservationToken, &attempt.PassNumber)
	})
	if err != nil {
		return EvidenceDiscoveryAttempt{}, fmt.Errorf("evidence discovery target lock: reserve pass: %w", err)
	}
	return attempt, nil
}

// MarkEvidenceDiscoveryAttemptDispatched records that the provider call is
// about to begin. Such reservations are not safe to reclaim after an
// uncertain response because the provider result may have been valid.
func (r *SemanticRepositoryImpl) MarkEvidenceDiscoveryAttemptDispatched(
	ctx context.Context,
	input EvidenceDiscoveryAttemptValidationInput,
) error {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	input.ReservationToken = strings.TrimSpace(input.ReservationToken)
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.AttemptID); err != nil {
		return fmt.Errorf("attempt_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.ReservationToken); err != nil {
		return fmt.Errorf("reservation_token is required: %w", err)
	}
	err := r.withDreamWriteTx(ctx, input.TeamID, "", true, func(tx *gorm.DB) error {
		result := tx.WithContext(ctx).Exec(`
			UPDATE dream_evidence_target_attempts
			SET dispatch_started_at = COALESCE(dispatch_started_at, now()), updated_at = now()
			WHERE team_id = ?::uuid AND attempt_id = ?::uuid
			  AND reservation_token = ?::uuid AND status = 'reserved'
		`, input.TeamID, input.AttemptID, input.ReservationToken)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrEvidenceDiscoveryAttemptNotReserved
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("dream: mark evidence discovery dispatch: %w", err)
	}
	return nil
}

func (r *SemanticRepositoryImpl) MarkEvidenceDiscoveryAttemptValidated(
	ctx context.Context,
	input EvidenceDiscoveryAttemptValidationInput,
) error {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	input.ReservationToken = strings.TrimSpace(input.ReservationToken)
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.AttemptID); err != nil {
		return fmt.Errorf("attempt_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.ReservationToken); err != nil {
		return fmt.Errorf("reservation_token is required: %w", err)
	}
	if input.AcceptedProposals < 0 || input.AcceptedProposals > 50 {
		return errors.New("accepted_proposals must be between zero and fifty")
	}
	err := r.withDreamWriteTx(ctx, input.TeamID, "", true, func(tx *gorm.DB) error {
		res := tx.WithContext(ctx).Exec(`
			UPDATE dream_evidence_target_attempts
			SET status = 'validated', dispatch_started_at = COALESCE(dispatch_started_at, now()),
			    accepted_proposals = ?, validated_at = now(), updated_at = now()
			WHERE team_id = ?::uuid AND attempt_id = ?::uuid
			  AND reservation_token = ?::uuid AND status = 'reserved'
		`, input.AcceptedProposals, input.TeamID, input.AttemptID, input.ReservationToken)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 1 {
			return nil
		}
		var status, storedToken string
		err := tx.WithContext(ctx).Raw(`
			SELECT status, reservation_token::text FROM dream_evidence_target_attempts
			WHERE team_id = ?::uuid AND attempt_id = ?::uuid
		`, input.TeamID, input.AttemptID).Row().Scan(&status, &storedToken)
		if err == nil && status == "validated" && storedToken == input.ReservationToken {
			return nil
		}
		if errors.Is(err, sql.ErrNoRows) {
			return ErrEvidenceDiscoveryAttemptNotReserved
		}
		if err != nil {
			return err
		}
		return ErrEvidenceDiscoveryAttemptNotReserved
	})
	if err != nil {
		return fmt.Errorf("dream: validate evidence discovery attempt: %w", err)
	}
	return nil
}

func (r *SemanticRepositoryImpl) AbandonEvidenceDiscoveryAttempt(
	ctx context.Context,
	teamID, attemptID, reservationToken string,
) error {
	teamID, attemptID, reservationToken = strings.TrimSpace(teamID), strings.TrimSpace(attemptID), strings.TrimSpace(reservationToken)
	if _, err := uuid.Parse(teamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(attemptID); err != nil {
		return fmt.Errorf("attempt_id is required: %w", err)
	}
	if _, err := uuid.Parse(reservationToken); err != nil {
		return fmt.Errorf("reservation_token is required: %w", err)
	}
	err := r.withDreamWriteTx(ctx, teamID, "", true, func(tx *gorm.DB) error {
		return tx.WithContext(ctx).Exec(`
			UPDATE dream_evidence_target_attempts
			SET status = 'abandoned', abandoned_at = now(), reservation_expires_at = now(), updated_at = now()
			WHERE team_id = ?::uuid AND attempt_id = ?::uuid
			  AND reservation_token = ?::uuid AND status = 'reserved'
		`, teamID, attemptID, reservationToken).Error
	})
	if err != nil {
		return fmt.Errorf("dream: abandon evidence discovery attempt: %w", err)
	}
	return nil
}
