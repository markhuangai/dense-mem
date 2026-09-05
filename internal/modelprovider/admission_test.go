package modelprovider

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type admissionTestContextKey struct{}

func TestAdmissionCallbackPreservesContextAndErrors(t *testing.T) {
	ctx := context.WithValue(context.Background(), admissionTestContextKey{}, "value")
	wantErr := errors.New("admission failed")
	called := false
	withCallback := WithAdmissionCallback(ctx, func(got context.Context) error {
		called = true
		require.Equal(t, "value", got.Value(admissionTestContextKey{}))
		return wantErr
	})

	require.ErrorIs(t, NotifyAdmission(withCallback), wantErr)
	require.True(t, called)
	require.NoError(t, NotifyAdmission(ctx))
	require.NoError(t, NotifyAdmission(context.TODO()))
}

func TestWithAdmissionCallbackIgnoresNilCallback(t *testing.T) {
	ctx := context.Background()
	require.Equal(t, ctx, WithAdmissionCallback(ctx, nil))
}
