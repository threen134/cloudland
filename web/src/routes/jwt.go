/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

History:
   Date     Who ID    Description
   -------- --- ---   -----------
   01/13/19 nanjj  Initial code

*/

package routes

import (
	"crypto/rsa"
	"fmt"
	"time"

	"math/rand"

	"web/src/model"

	jwt "github.com/golang-jwt/jwt/v4"
	"github.com/spf13/viper"
)

var (
	_publicKey  *rsa.PublicKey
	_privateKey *rsa.PrivateKey
)

type CustomClaims struct {
	jwt.RegisteredClaims
	UID string           `json:"uid,omitempty"`
	OID string           `json:"oid,omitempty"`
	SR  model.SystemRole `json:"sr"`
	OR  model.OrgRole    `json:"or"`
	ST  model.UserStatus `json:"st"`
}

func (*CustomClaims) verifyPrivilege(resource interface{}) (result bool) {
	logger.Infof("ENTER verifyPrivilege: resource=%v", resource)
	defer func() {
		logger.Infof("EXIT verifyPrivilege: result=%v", result)
	}()
	// TODO:  checkout authority
	return true
}

func NewClaims(u, o, uid, oid string, sysRole model.SystemRole, orgRole model.OrgRole, status model.UserStatus) (claims jwt.Claims, issuedAt, ExpiresAt int64) {
	logger.Infof("ENTER NewClaims: u=%s, o=%s, uid=%s, oid=%s, sysRole=%v, orgRole=%v, status=%v", u, o, uid, oid, sysRole, orgRole, status)
	defer func() {
		logger.Infof("EXIT NewClaims: issuedAt=%d, ExpiresAt=%d", issuedAt, ExpiresAt)
	}()
	now := time.Now()
	issuedAt = now.Unix()
	ExpiresAt = now.Add(time.Hour * 2).Unix()
	claims = &CustomClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Audience:  jwt.ClaimStrings{u},
			ExpiresAt: jwt.NewNumericDate(time.Unix(ExpiresAt, 0)),
			ID:        claimsID(now),
			IssuedAt:  jwt.NewNumericDate(time.Unix(issuedAt, 0)),
			Issuer:    "Cloudland",
			NotBefore: jwt.NewNumericDate(time.Unix(issuedAt, 0)),
			Subject:   o,
		},
		UID: uid,
		OID: oid,
		SR:  sysRole,
		OR:  orgRole,
		ST:  status,
	}
	return
}

func claimsID(now time.Time) (id string) {
	logger.Infof("ENTER claimsID: now=%v", now)
	defer func() {
		logger.Infof("EXIT claimsID: Returns: id=%s", id)
	}()
	return fmt.Sprintf("%d", now.UnixNano()+rand.Int63())
}

func init() {
	rand.Seed(time.Now().UnixNano())
}

func publicKey() (key *rsa.PublicKey) {
	logger.Info("ENTER publicKey: get public key")
	defer func() {
		logger.Info("EXIT publicKey")
	}()
	if _publicKey == nil {
		keyStr := viper.GetString("key.public")
		if keyStr == "" {
			panic("No public key provided")
		}
		var err error
		_publicKey, err = jwt.ParseRSAPublicKeyFromPEM([]byte(keyStr))
		if err != nil {
			panic(err)
		}
	}
	return _publicKey
}

func privateKey() (key *rsa.PrivateKey) {
	logger.Info("ENTER privateKey: get private key")
	defer func() {
		logger.Info("EXIT privateKey")
	}()
	if _privateKey == nil {
		keyStr := viper.GetString("key.private")
		if keyStr == "" {
			panic("No private key provided")
		}
		var err error
		_privateKey, err = jwt.ParseRSAPrivateKeyFromPEM([]byte(keyStr))
		if err != nil {
			panic(err)
		}

	}
	return _privateKey
}

func NewToken(u, o, uid, oid string, sysRole model.SystemRole, orgRole model.OrgRole, status model.UserStatus) (signed string, issueAt, expiresAt int64, err error) {
	logger.Infof("ENTER NewToken: u=%s, o=%s, uid=%s, oid=%s, sysRole=%v, orgRole=%v, status=%v", u, o, uid, oid, sysRole, orgRole, status)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT NewToken: Error: %v", err)
		} else {
			logger.Infof("EXIT NewToken: issueAt=%d, expiresAt=%d", issueAt, expiresAt)
		}
	}()
	var claims jwt.Claims
	claims, issueAt, expiresAt = NewClaims(u, o, uid, oid, sysRole, orgRole, status)
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err = token.SignedString(privateKey())
	return
}

func ParseToken(tokenString string) (token *jwt.Token, tokenClaims *CustomClaims, err error) {
	logger.Infof("ENTER ParseToken: tokenString=%s", tokenString)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT ParseToken: Error: %v", err)
		} else {
			logger.Infof("EXIT ParseToken: Parsed successfully")
		}
	}()
	tokenClaims = &CustomClaims{}
	token, err = jwt.ParseWithClaims(
		tokenString,
		tokenClaims,
		func(token *jwt.Token) (interface{}, error) {
			return publicKey(), nil
		},
	)
	return
}
