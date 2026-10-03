package grpcauth

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestValidate(t *testing.T) {
	withAuth := func(value string) context.Context {
		return metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", value))
	}
	cases := []struct {
		name     string
		ctx      context.Context
		expected string
		ok       bool
	}{
		{"auth disabled", context.Background(), "", true},
		{"no metadata", context.Background(), "secret", false},
		{"wrong token", withAuth("Bearer other"), "secret", false},
		{"missing bearer prefix", withAuth("secret"), "secret", false},
		{"valid token", withAuth("Bearer secret"), "secret", true},
	}
	for _, c := range cases {
		err := Validate(c.ctx, c.expected)
		if c.ok && err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
		}
		if !c.ok && status.Code(err) != codes.Unauthenticated {
			t.Errorf("%s: got %v, want Unauthenticated", c.name, err)
		}
	}
}
