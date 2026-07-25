package service

import "context"

// ProfileDataPurger removes profile-owned non-primary state. PostgreSQL is the
// only durable authority after cutover, so the default implementation is empty.
type ProfileDataPurger interface {
	PurgeProfileData(ctx context.Context, profileID string) error
}

type noopProfileDataPurger struct{}

func NewProfileDataPurger() ProfileDataPurger {
	return noopProfileDataPurger{}
}

func (noopProfileDataPurger) PurgeProfileData(context.Context, string) error {
	return nil
}
