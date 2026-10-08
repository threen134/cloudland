package apis

import "testing"

// D4: migrations name instances of every organization and the hosts they move between, so the gateway lets only
// system admins reach them, like clapi does
func TestMigrationRoutesAreForSystemAdmins(t *testing.T) {
	want := map[string]bool{"GET /migrations": false, "POST /migrations": false, "GET /migrations/:id": false,
		"GET /instances/:id/migration_targets": false}
	for _, route := range proxyRoutes {
		key := route.Method + " " + route.Path
		if _, ok := want[key]; !ok {
			continue
		}
		want[key] = true
		if !route.Admin {
			t.Errorf("%s is open to every member", key)
		}
	}
	for key, found := range want {
		if !found {
			t.Errorf("%s is not proxied", key)
		}
	}
}
