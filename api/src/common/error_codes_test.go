/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package common

import (
	"net/http"
	"testing"
)

// Refusals a caller can act on must not surface as 500 (1.1.0 regression: deleting the primary interface did)
func TestRefusalStatus(t *testing.T) {
	for code, want := range map[ErrCode]int{
		ErrCannotDeletePrimaryInterface: http.StatusBadRequest,
		ErrPermissionDenied:             http.StatusForbidden,
		ErrInvalidParameter:             http.StatusBadRequest,
		ErrHypervisorInvalidState:       http.StatusBadRequest,
	} {
		if got := code.ToHTTPStatus(); got != want {
			t.Errorf("%s: status %d, want %d", code, got, want)
		}
	}
}
