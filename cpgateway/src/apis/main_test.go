package apis

import (
	"os"
	"testing"

	"cpgateway/src/dbs"
)

// TestMain removes the throwaway SQLite database the tests of this package share (see dbs.testSQLiteURI).
func TestMain(m *testing.M) {
	code := m.Run()
	dbs.RemoveTestDB()
	os.Exit(code)
}
