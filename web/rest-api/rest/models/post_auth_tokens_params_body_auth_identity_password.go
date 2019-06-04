// Code generated by go-swagger; DO NOT EDIT.

package models

// This file was generated by the swagger tool.
// Editing this file might prove futile when you re-run the swagger generate command

import (
	strfmt "github.com/go-openapi/strfmt"

	"github.com/go-openapi/errors"
	"github.com/go-openapi/swag"
)

// PostAuthTokensParamsBodyAuthIdentityPassword post auth tokens params body auth identity password
// swagger:model postAuthTokensParamsBodyAuthIdentityPassword
type PostAuthTokensParamsBodyAuthIdentityPassword struct {

	// user
	User *PostAuthTokensParamsBodyAuthIdentityPasswordUser `json:"user,omitempty"`
}

// Validate validates this post auth tokens params body auth identity password
func (m *PostAuthTokensParamsBodyAuthIdentityPassword) Validate(formats strfmt.Registry) error {
	var res []error

	if err := m.validateUser(formats); err != nil {
		res = append(res, err)
	}

	if len(res) > 0 {
		return errors.CompositeValidationError(res...)
	}
	return nil
}

func (m *PostAuthTokensParamsBodyAuthIdentityPassword) validateUser(formats strfmt.Registry) error {

	if swag.IsZero(m.User) { // not required
		return nil
	}

	if m.User != nil {
		if err := m.User.Validate(formats); err != nil {
			if ve, ok := err.(*errors.Validation); ok {
				return ve.ValidateName("user")
			}
			return err
		}
	}

	return nil
}

// MarshalBinary interface implementation
func (m *PostAuthTokensParamsBodyAuthIdentityPassword) MarshalBinary() ([]byte, error) {
	if m == nil {
		return nil, nil
	}
	return swag.WriteJSON(m)
}

// UnmarshalBinary interface implementation
func (m *PostAuthTokensParamsBodyAuthIdentityPassword) UnmarshalBinary(b []byte) error {
	var res PostAuthTokensParamsBodyAuthIdentityPassword
	if err := swag.ReadJSON(b, &res); err != nil {
		return err
	}
	*m = res
	return nil
}
