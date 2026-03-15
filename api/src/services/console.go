/*
Copyright <holder> All Rights Reserved.

*/

package services

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	. "api/src/common"
	"api/src/model"

	jwt "github.com/golang-jwt/jwt/v4"
	"golang.org/x/crypto/sha3"
)

const (
	TokenExpireDuration = time.Hour * 2
)

// Randomly generate a string of length 10
func RandomStr() (res string) {
	logger.Info("ENTER RandomStr: generate random string")
	defer func() {
		logger.Infof("EXIT RandomStr: resultLength=%d", len(res))
	}()
	str := "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	bytes := []byte(str)
	result := []byte{}
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	for i := 0; i < 10; i++ {
		result = append(result, bytes[r.Intn(len(bytes))])
	}
	return string(result)
}

func MakeToken(ctx context.Context, instance *model.Instance) (token string, err error) {
	logger.Infof("ENTER MakeToken: instanceID=%d", instance.ID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT MakeToken: error=%v", err)
		} else {
			logger.Infof("EXIT MakeToken: tokenGenerated")
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckOrgPermission(model.OrgWriter)
	if !permit {
		logger.Error("Not authorized to create interface in public subnet")
		return "", NewCLError(ErrPermissionDenied, "Not authorized to create interface in public subnet", nil)
	}
	secret := RandomStr()
	tkClaim := TokenClaim{
		OrgID:      memberShip.OrgID,
		OrgRole:    memberShip.OrgRole,
		InstanceID: int(instance.ID),
		Secret:     secret,
	}
	tkClaim.RegisteredClaims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(TokenExpireDuration))
	tokenHash := make([]byte, 32)
	data := sha3.NewShake256()
	data.Write([]byte(secret))
	data.Read(tokenHash)
	hashSecret := fmt.Sprintf("%x", tokenHash)
	ctx, db := GetContextDB(ctx)
	console := &model.Console{
		Instance:   instance.ID,
		Type:       "vnc",
		HashSecret: hashSecret,
	}
	err = db.Where("instance = ?", instance.ID).Assign(console).FirstOrCreate(&model.Console{}).Error
	if err != nil {
		logger.Error("Failed to make console record ", err)
		return "", NewCLError(ErrConsoleCreateFailed, "Failed to make console record", err)
	}
	tokenClaim := jwt.NewWithClaims(jwt.SigningMethodHS256, tkClaim)
	token, err = tokenClaim.SignedString(SignedSeret)
	return
}

func ResolveToken(ctx context.Context, tokenString string) (instanceID int, memberShip *MemberShip, err error) {
	logger.Infof("ENTER ResolveToken: tokenLength=%d", len(tokenString))
	defer func() {
		if err != nil {
			logger.Errorf("EXIT ResolveToken: error=%v", err)
		} else {
			logger.Infof("EXIT ResolveToken: instanceID=%d", instanceID)
		}
	}()
	token, err := jwt.ParseWithClaims(tokenString, &TokenClaim{}, func(token *jwt.Token) (interface{}, error) {
		return SignedSeret, nil
	})
	if err != nil || token == nil {
		return 0, nil, err
	}
	claims, ok := token.Claims.(*TokenClaim)
	if !ok || !token.Valid {
		return 0, nil, NewCLError(ErrInvalidConsoleToken, "Token is invalid", nil)
	}
	ctx, db := GetContextDB(ctx)
	instanceID = claims.InstanceID
	console := &model.Console{Instance: int64(instanceID)}
	err = db.Where(console).Take(console).Error
	if err != nil {
		return 0, nil, NewCLError(ErrConsoleNotFound, "Failed to retrieve console record", err)
	}
	tokenHash := make([]byte, 32)
	data := sha3.NewShake256()
	data.Write([]byte(claims.Secret))
	data.Read(tokenHash)
	hashSecret := fmt.Sprintf("%x", tokenHash)
	if hashSecret != console.HashSecret {
		return 0, nil, NewCLError(ErrInvalidConsoleToken, "Secret can not pass validation", nil)
	}
	memberShip = &MemberShip{
		OrgID:   claims.OrgID,
		OrgRole: claims.OrgRole,
	}
	return instanceID, memberShip, nil
}
