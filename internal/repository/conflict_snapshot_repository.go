package repository

import (
	"database/sql"
	"time"
)

func conflictTimesEqualAtDatabasePrecision(left, right time.Time) bool {
	return left.UTC().Truncate(time.Microsecond).Equal(right.UTC().Truncate(time.Microsecond))
}

func conflictNullableTimesEqual(left sql.NullTime, right *time.Time) bool {
	if !left.Valid {
		return right == nil
	}
	return right != nil && conflictTimesEqualAtDatabasePrecision(left.Time, *right)
}
