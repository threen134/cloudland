/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"context"
	"errors"
	"strings"
	"testing"

	. "api/src/common"
	"api/src/model"
)

// A reader asking for a console is refused before any record is written, with a message about the console
// (it was a message copied from the subnet code)
func TestMakeTokenRefusesReaders(t *testing.T) {
	ctx := context.WithValue(context.Background(), "membership", &MemberShip{OrgID: 5, OrgRole: model.OrgReader})
	for _, consoleType := range []string{ConsoleTypeVNC, ConsoleTypeSerial} {
		token, err := MakeToken(ctx, &model.Instance{Model: model.Model{ID: 3}, Owner: 5}, consoleType)
		var clErr *CLError
		if token != "" || !errors.As(err, &clErr) || clErr.Code != ErrPermissionDenied {
			t.Fatalf("%s: token %q, error %v; want a permission error", consoleType, token, err)
		}
		if !strings.Contains(clErr.Message, "console") || strings.Contains(clErr.Message, "subnet") {
			t.Errorf("%s: message %q is not about the console", consoleType, clErr.Message)
		}
	}
}
