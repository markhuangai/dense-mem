package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestHypothesisConfirmationLockClassifiesInvalidHypothesisID(t *testing.T) {
	repo := &Store{db: &gorm.DB{}}
	err := repo.WithHypothesisConfirmationLock(context.Background(), uuid.NewString(), "not-a-uuid", func(DreamRepository) error {
		return nil
	})
	require.ErrorIs(t, err, ErrDreamHypothesisIDInvalid)
}
