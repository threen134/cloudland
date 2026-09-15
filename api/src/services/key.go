/*
Copyright <holder> All Rights Reserved.
SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"

	"golang.org/x/crypto/ssh"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"
)

var KeyAdmin = &KeyAdminService{}

type KeyAdminService struct{}

func (a *KeyAdminService) CreateKeyPair(ctx context.Context) (publicKey, fingerPrint, privateKey string, err error) {
	logger.Ctx(ctx).Infof("ENTER KeyAdmin.CreateKeyPair")
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT KeyAdmin.CreateKeyPair: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT KeyAdmin.CreateKeyPair: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckOrgPermission(model.OrgWriter)
	if !permit {
		logger.Ctx(ctx).Error("Not authorized to create keys")
		err = NewCLError(ErrPermissionDenied, "Not authorized to create keys", nil)
		return
	}
	// generate key
	private, er := rsa.GenerateKey(rand.Reader, 1024)
	if er != nil {
		logger.Ctx(ctx).Error("failed to create privateKey ")
		err = er
		return
	}
	privateKeyPEM := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(private)}
	privateKey = string(pem.EncodeToMemory(privateKeyPEM))
	pub, er := ssh.NewPublicKey(&private.PublicKey)
	if er != nil {
		logger.Ctx(ctx).Error("failed to create publicKey")
		err = NewCLError(ErrSSHKeyGenerateFailed, "Failed to create public key", er)
		return
	}
	temp := ssh.MarshalAuthorizedKey(pub)
	publicKey = string(temp)
	fingerPrint = ssh.FingerprintLegacyMD5(pub)
	return
}

func (a *KeyAdminService) Create(ctx context.Context, name, publicKey, uuid string) (key *model.Key, err error) {
	logger.Ctx(ctx).Infof("ENTER KeyAdmin.Create: name=%s, uuid=%s", name, uuid)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT KeyAdmin.Create: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT KeyAdmin.Create: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckOrgPermission(model.OrgWriter)
	if !permit {
		logger.Ctx(ctx).Error("Not authorized to create keys")
		err = NewCLError(ErrPermissionDenied, "Not authorized to create keys", nil)
		return
	}
	pub, _, _, _, puberr := ssh.ParseAuthorizedKey([]byte(publicKey))
	if puberr != nil {
		logger.Ctx(ctx).Error("Invalid public key")
		err = NewCLError(ErrInvalidParameter, "Invalid public key", puberr)
		return
	}
	fingerPrint := ssh.FingerprintLegacyMD5(pub)
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	key = &model.Key{Model: model.Model{Creater: memberShip.UserID}, Owner: memberShip.OrgID, Name: name, PublicKey: publicKey, FingerPrint: fingerPrint}
	if uuid != "" {
		logger.Ctx(ctx).Infof("Creating new ssh key with uuid %s", uuid)
		key.UUID = uuid
	}
	err = db.Create(key).Error
	if err != nil {
		logger.Ctx(ctx).Error("DB failed to create key, %v", err)
		err = NewCLError(ErrSSHKeyCreateFailed, "Failed to create key", err)
		return
	}
	return
}

func (a *KeyAdminService) Delete(ctx context.Context, key *model.Key) (err error) {
	logger.Ctx(ctx).Infof("ENTER KeyAdmin.Delete: keyID=%d, uuid=%s", key.ID, key.UUID)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT KeyAdmin.Delete: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT KeyAdmin.Delete: success")
		}
	}()
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckResourceOrg(model.OrgWriter, key.Owner)
	if !permit {
		logger.Ctx(ctx).Error("Not authorized to delete the key")
		err = NewCLError(ErrPermissionDenied, "Not authorized to delete the key", nil)
		return
	}
	err = db.Model(key).Association("Instances").Find(&key.Instances)
	if err != nil {
		logger.Ctx(ctx).Error("Failed to count the number of instances using the key", err)
		err = NewCLError(ErrSQLSyntaxError, "Failed to count the number of instances using the key", err)
		return
	}
	if len(key.Instances) > 0 {
		logger.Ctx(ctx).Error("Key can not be deleted if there are instances using it")
		err = NewCLError(ErrSSHKeyInUse, "Key can not be deleted if there are instances using it", nil)
		return
	}
	if err = db.Delete(key).Error; err != nil {
		logger.Ctx(ctx).Error("DB failed to delete key ", err)
		err = NewCLError(ErrSSHKeyDeleteFailed, "Failed to delete key", err)
		return
	}
	key.Name = fmt.Sprintf("%s-%d", key.Name, key.CreatedAt.Unix())
	err = db.Model(&model.Key{}).Unscoped().Where("id = ?", key.ID).Update("name", key.Name).Error
	if err != nil {
		logger.Ctx(ctx).Error("DB failed to update key name", err)
		err = NewCLError(ErrSSHKeyUpdateFailed, "Failed to update key name", err)
		return
	}
	return
}

func (a *KeyAdminService) Get(ctx context.Context, id int64) (key *model.Key, err error) {
	logger.Ctx(ctx).Infof("ENTER KeyAdmin.Get: id=%d", id)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT KeyAdmin.Get: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT KeyAdmin.Get: success")
		}
	}()
	if id <= 0 {
		err = NewCLError(ErrInvalidParameter, fmt.Sprintf("Invalid key ID: %d", id), nil)
		logger.Ctx(ctx).Error(err)
		return
	}
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	where, args := memberShip.GetOrgFilter()
	key = &model.Key{Model: model.Model{ID: id}}
	if err = db.Where(where, args...).Take(key).Error; err != nil {
		logger.Ctx(ctx).Error("Failed to query key, %v", err)
		err = NewCLError(ErrSSHKeyNotFound, "Failed to query key", err)
		return
	}
	return
}

func (a *KeyAdminService) GetKeyByUUID(ctx context.Context, uuID string) (key *model.Key, err error) {
	logger.Ctx(ctx).Infof("ENTER KeyAdmin.GetKeyByUUID: uuID=%s", uuID)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT KeyAdmin.GetKeyByUUID: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT KeyAdmin.GetKeyByUUID: success")
		}
	}()
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	where, args := memberShip.GetOrgFilter()
	key = &model.Key{}
	err = db.Where(where, args...).Where("uuid = ?", uuID).Take(key).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to query key, %v", err)
		return
	}
	return
}

func (a *KeyAdminService) GetKeyByName(ctx context.Context, name string) (key *model.Key, err error) {
	logger.Ctx(ctx).Infof("ENTER KeyAdmin.GetKeyByName: name=%s", name)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT KeyAdmin.GetKeyByName: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT KeyAdmin.GetKeyByName: success")
		}
	}()
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	where, args := memberShip.GetOrgFilter()
	key = &model.Key{}
	err = db.Where(where, args...).Where("name = ?", name).Take(key).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to query key, %v", err)
		return
	}
	return
}

func (a *KeyAdminService) GetKey(ctx context.Context, reference *BaseReference) (key *model.Key, err error) {
	logger.Ctx(ctx).Infof("ENTER KeyAdmin.GetKey: reference=%+v", reference)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT KeyAdmin.GetKey: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT KeyAdmin.GetKey: success")
		}
	}()
	if reference == nil || (reference.ID == "" && reference.Name == "") {
		err = NewCLError(ErrInvalidParameter, "Key base reference must be provided with either uuid or name", nil)
		return
	}
	if reference.ID != "" {
		key, err = a.GetKeyByUUID(ctx, reference.ID)
		return
	}
	if reference.Name != "" {
		key, err = a.GetKeyByName(ctx, reference.Name)
		return
	}
	return
}

func (a *KeyAdminService) List(ctx context.Context, offset, limit int64, order, query string) (total int64, keys []*model.Key, err error) {
	logger.Ctx(ctx).Infof("ENTER KeyAdmin.List: offset=%d, limit=%d, order=%s, query=%s", offset, limit, order, query)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT KeyAdmin.List: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT KeyAdmin.List: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckOrgPermission(model.OrgReader)
	if !permit {
		logger.Ctx(ctx).Error("Not authorized for this operation")
		err = NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
		return
	}
	ctx, db := GetContextDB(ctx)
	if limit == 0 {
		limit = 16
	}

	if order == "" {
		order = "created_at"
	}

	if query != "" {
		query = fmt.Sprintf("name like '%%%s%%'", query)
	}
	queryBuilder, args := memberShip.GetOrgFilter()
	keys = []*model.Key{}
	if err = db.Model(&model.Key{}).Where(queryBuilder, args...).Where(query).Count(&total).Error; err != nil {
		logger.Ctx(ctx).Error("DB failed to count keys, %v", err)
		err = NewCLError(ErrSQLSyntaxError, "Failed to count keys", err)
		return
	}
	db = dbs.Sortby(db.Offset(int(offset)).Limit(int(limit)), order)
	if err = db.Where(queryBuilder, args...).Where(query).Find(&keys).Error; err != nil {
		logger.Ctx(ctx).Error("DB failed to query keys, %v", err)
		err = NewCLError(ErrSQLSyntaxError, "Failed to query keys", err)
		return
	}
	permit = memberShip.IsSystemAdmin()
	if permit {
		_, db = GetContextDB(ctx) // 链式调用复用 Statement，取新会话查询 OwnerInfo
		for _, key := range keys {
			key.OwnerInfo = &model.Organization{Model: model.Model{ID: key.Owner}}
			if err = db.Take(key.OwnerInfo).Error; err != nil {
				logger.Ctx(ctx).Error("Failed to query owner info", err)
				err = nil
				continue
			}
		}
	}
	return
}
