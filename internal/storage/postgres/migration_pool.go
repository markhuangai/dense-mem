package postgres

import (
	"database/sql"
	"sync"
)

var migrationPools = struct {
	sync.Mutex
	users map[*sql.DB]int
}{users: map[*sql.DB]int{}}

func reserveMigrationPool(db *sql.DB) func() {
	if db == nil {
		return func() {}
	}
	migrationPools.Lock()
	defer migrationPools.Unlock()
	if migrationPools.users[db] == 0 {
		if db.Stats().MaxOpenConnections != 1 {
			return func() {}
		}
		// Goose holds its lock connection while transaction-disabled migrations reserve theirs.
		db.SetMaxOpenConns(2)
	}
	migrationPools.users[db]++
	return func() {
		migrationPools.Lock()
		defer migrationPools.Unlock()
		migrationPools.users[db]--
		if migrationPools.users[db] == 0 {
			delete(migrationPools.users, db)
			db.SetMaxOpenConns(1)
		}
	}
}
