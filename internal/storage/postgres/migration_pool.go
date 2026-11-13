package postgres

import (
	"database/sql"
	"sync"
)

var migrationPools = struct {
	sync.Mutex
	users map[*sql.DB]*migrationPoolReservation
}{users: map[*sql.DB]*migrationPoolReservation{}}

type migrationPoolReservation struct{ limit, users int }

func reserveMigrationPool(db *sql.DB) func() {
	if db == nil {
		return func() {}
	}
	migrationPools.Lock()
	defer migrationPools.Unlock()
	reservation := migrationPools.users[db]
	if reservation == nil {
		limit := db.Stats().MaxOpenConnections
		if limit == 0 {
			return func() {}
		}
		reservation = &migrationPoolReservation{limit: limit}
		migrationPools.users[db] = reservation
	}
	reservation.users++
	// Each waiting Goose locker pins a connection, so retain one additional executor slot.
	db.SetMaxOpenConns(max(reservation.limit, reservation.users+1))
	return func() {
		migrationPools.Lock()
		defer migrationPools.Unlock()
		reservation.users--
		if reservation.users == 0 {
			delete(migrationPools.users, db)
			db.SetMaxOpenConns(reservation.limit)
		} else {
			db.SetMaxOpenConns(max(reservation.limit, reservation.users+1))
		}
	}
}
