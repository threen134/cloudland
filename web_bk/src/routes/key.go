/*
Copyright <holder> All Rights Reserved.
SPDX-License-Identifier: Apache-2.0
*/

package routes

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"strconv"

	"golang.org/x/crypto/ssh"

	. "web/src/common"
	"web/src/dbs"
	"web/src/model"

	"github.com/go-macaron/session"
	macaron "gopkg.in/macaron.v1"
)

var (
	keyAdmin = &KeyAdmin{}
	keyView  = &KeyView{}
)

type KeyAdmin struct{}
type KeyView struct{}

func (a *KeyAdmin) CreateKeyPair(ctx context.Context) (publicKey, fingerPrint, privateKey string, err error) {
	logger.Infof("ENTER KeyAdmin.CreateKeyPair")
	defer func() {
		if err != nil {
			logger.Errorf("EXIT KeyAdmin.CreateKeyPair: error=%v", err)
		} else {
			logger.Info("EXIT KeyAdmin.CreateKeyPair: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckOrgPermission(model.OrgWriter)
	if !permit {
		logger.Error("Not authorized to create keys")
		err = NewCLError(ErrPermissionDenied, "Not authorized to create keys", nil)
		return
	}
	// generate key
	private, er := rsa.GenerateKey(rand.Reader, 1024)
	if er != nil {
		logger.Error("failed to create privateKey ")
		err = er
		return
	}
	privateKeyPEM := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(private)}
	privateKey = string(pem.EncodeToMemory(privateKeyPEM))
	pub, er := ssh.NewPublicKey(&private.PublicKey)
	if er != nil {
		logger.Error("failed to create publicKey")
		err = NewCLError(ErrSSHKeyGenerateFailed, "Failed to create public key", er)
		return
	}
	temp := ssh.MarshalAuthorizedKey(pub)
	publicKey = string(temp)
	fingerPrint = ssh.FingerprintLegacyMD5(pub)
	return
}

func (a *KeyAdmin) Create(ctx context.Context, name, publicKey, uuid string) (key *model.Key, err error) {
	logger.Infof("ENTER KeyAdmin.Create: name=%s, uuid=%s", name, uuid)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT KeyAdmin.Create: error=%v", err)
		} else {
			logger.Info("EXIT KeyAdmin.Create: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckOrgPermission(model.OrgWriter)
	if !permit {
		logger.Error("Not authorized to create keys")
		err = NewCLError(ErrPermissionDenied, "Not authorized to create keys", nil)
		return
	}
	pub, _, _, _, puberr := ssh.ParseAuthorizedKey([]byte(publicKey))
	if puberr != nil {
		logger.Error("Invalid public key")
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
		logger.Infof("Creating new ssh key with uuid %s", uuid)
		key.UUID = uuid
	}
	err = db.Create(key).Error
	if err != nil {
		logger.Error("DB failed to create key, %v", err)
		err = NewCLError(ErrSSHKeyCreateFailed, "Failed to create key", err)
		return
	}
	return
}

func (a *KeyAdmin) Delete(ctx context.Context, key *model.Key) (err error) {
	logger.Infof("ENTER KeyAdmin.Delete: keyID=%d, uuid=%s", key.ID, key.UUID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT KeyAdmin.Delete: error=%v", err)
		} else {
			logger.Info("EXIT KeyAdmin.Delete: success")
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
		logger.Error("Not authorized to delete the key")
		err = NewCLError(ErrPermissionDenied, "Not authorized to delete the key", nil)
		return
	}
	err = db.Model(key).Related(&key.Instances, "Instances").Error
	if err != nil {
		logger.Error("Failed to count the number of instances using the key", err)
		err = NewCLError(ErrSQLSyntaxError, "Failed to count the number of instances using the key", err)
		return
	}
	if len(key.Instances) > 0 {
		logger.Error("Key can not be deleted if there are instances using it")
		err = NewCLError(ErrSSHKeyInUse, "Key can not be deleted if there are instances using it", nil)
		return
	}
	key.Name = fmt.Sprintf("%s-%d", key.Name, key.CreatedAt.Unix())
	err = db.Model(key).Update("name", key.Name).Error
	if err != nil {
		logger.Error("DB failed to update key name", err)
		err = NewCLError(ErrSSHKeyUpdateFailed, "Failed to update key name", err)
		return
	}
	if err = db.Delete(key).Error; err != nil {
		logger.Error("DB failed to delete key ", err)
		err = NewCLError(ErrSSHKeyDeleteFailed, "Failed to delete key", err)
		return
	}
	return
}

func (a *KeyAdmin) Get(ctx context.Context, id int64) (key *model.Key, err error) {
	logger.Infof("ENTER KeyAdmin.Get: id=%d", id)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT KeyAdmin.Get: error=%v", err)
		} else {
			logger.Info("EXIT KeyAdmin.Get: success")
		}
	}()
	if id <= 0 {
		err = NewCLError(ErrInvalidParameter, fmt.Sprintf("Invalid key ID: %d", id), nil)
		logger.Error(err)
		return
	}
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	where, args := memberShip.GetOrgFilter()
	key = &model.Key{Model: model.Model{ID: id}}
	if err = db.Where(where, args...).Take(key).Error; err != nil {
		logger.Error("Failed to query key, %v", err)
		err = NewCLError(ErrSSHKeyNotFound, "Failed to query key", err)
		return
	}
	return
}

func (a *KeyAdmin) GetKeyByUUID(ctx context.Context, uuID string) (key *model.Key, err error) {
	logger.Infof("ENTER KeyAdmin.GetKeyByUUID: uuID=%s", uuID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT KeyAdmin.GetKeyByUUID: error=%v", err)
		} else {
			logger.Info("EXIT KeyAdmin.GetKeyByUUID: success")
		}
	}()
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	where, args := memberShip.GetOrgFilter()
	key = &model.Key{}
	err = db.Where(where, args...).Where("uuid = ?", uuID).Take(key).Error
	if err != nil {
		logger.Error("Failed to query key, %v", err)
		return
	}
	return
}

func (a *KeyAdmin) GetKeyByName(ctx context.Context, name string) (key *model.Key, err error) {
	logger.Infof("ENTER KeyAdmin.GetKeyByName: name=%s", name)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT KeyAdmin.GetKeyByName: error=%v", err)
		} else {
			logger.Info("EXIT KeyAdmin.GetKeyByName: success")
		}
	}()
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	where, args := memberShip.GetOrgFilter()
	key = &model.Key{}
	err = db.Where(where, args...).Where("name = ?", name).Take(key).Error
	if err != nil {
		logger.Error("Failed to query key, %v", err)
		return
	}
	return
}

func (a *KeyAdmin) GetKey(ctx context.Context, reference *BaseReference) (key *model.Key, err error) {
	logger.Infof("ENTER KeyAdmin.GetKey: reference=%+v", reference)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT KeyAdmin.GetKey: error=%v", err)
		} else {
			logger.Info("EXIT KeyAdmin.GetKey: success")
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

func (a *KeyAdmin) List(ctx context.Context, offset, limit int64, order, query string) (total int64, keys []*model.Key, err error) {
	logger.Infof("ENTER KeyAdmin.List: offset=%d, limit=%d, order=%s, query=%s", offset, limit, order, query)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT KeyAdmin.List: error=%v", err)
		} else {
			logger.Info("EXIT KeyAdmin.List: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckOrgPermission(model.OrgReader)
	if !permit {
		logger.Error("Not authorized for this operation")
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
		logger.Error("DB failed to count keys, %v", err)
		err = NewCLError(ErrSQLSyntaxError, "Failed to count keys", err)
		return
	}
	db = dbs.Sortby(db.Offset(offset).Limit(limit), order)
	if err = db.Where(queryBuilder, args...).Where(query).Find(&keys).Error; err != nil {
		logger.Error("DB failed to query keys, %v", err)
		err = NewCLError(ErrSQLSyntaxError, "Failed to query keys", err)
		return
	}
	permit = memberShip.IsSystemAdmin()
	if permit {
		db = db.Offset(0).Limit(-1)
		for _, key := range keys {
			key.OwnerInfo = &model.Organization{Model: model.Model{ID: key.Owner}}
			if err = db.Take(key.OwnerInfo).Error; err != nil {
				logger.Error("Failed to query owner info", err)
				err = nil
				continue
			}
		}
	}
	return
}

func (v *KeyView) List(c *macaron.Context, store session.Store) {
	logger.Infof("ENTER KeyView.List: query=%s", c.Req.URL.RawQuery)
	defer logger.Info("EXIT KeyView.List")
	ctx := c.Req.Context()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckOrgPermission(model.OrgReader)
	if !permit {
		logger.Error("Not authorized for this operation")
		c.Data["ErrorMsg"] = "Not authorized for this operation"
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	offset := c.QueryInt64("offset")
	limit := c.QueryInt64("limit")
	if limit == 0 {
		limit = 16
	}
	order := c.QueryTrim("order")
	if order == "" {
		order = "-created_at"
	}
	query := c.QueryTrim("q")
	total, keys, err := keyAdmin.List(c.Req.Context(), offset, limit, order, query)
	if err != nil {
		logger.Error("Failed to list keys, %v", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(500, "500")
		return
	}
	c.Data["Keys"] = keys
	c.Data["Total"] = total
	c.Data["Pages"] = GetPages(total, limit)
	c.Data["Query"] = query
	c.HTML(200, "keys")
}

func (v *KeyView) Delete(c *macaron.Context, store session.Store) (err error) {
	logger.Infof("ENTER KeyView.Delete: id=%s", c.Params("id"))
	defer func() {
		if err != nil {
			logger.Errorf("EXIT KeyView.Delete: error=%v", err)
		} else {
			logger.Info("EXIT KeyView.Delete: success")
		}
	}()
	ctx := c.Req.Context()
	id := c.Params("id")
	if id == "" {
		c.Data["ErrorMsg"] = "Id is Empty"
		c.Error(http.StatusBadRequest)
		return
	}
	keyID, err := strconv.Atoi(id)
	if err != nil {
		logger.Error("Invalid key id, %v", err)
		c.Data["ErrorMsg"] = err.Error()
		c.Error(http.StatusBadRequest)
		return
	}
	key, err := keyAdmin.Get(ctx, int64(keyID))
	if err != nil {
		logger.Error("Failed to get key, %v", err)
		c.Data["ErrorMsg"] = err.Error()
		c.Error(http.StatusBadRequest)
		return
	}
	err = keyAdmin.Delete(ctx, key)
	if err != nil {
		logger.Error("Failed to delete key, %v", err)
		c.Data["ErrorMsg"] = err.Error()
		c.Error(http.StatusBadRequest)
		return
	}
	c.JSON(200, map[string]interface{}{
		"redirect": "keys",
	})
	return
}

func (v *KeyView) New(c *macaron.Context, store session.Store) {
	logger.Infof("ENTER KeyView.New: query=%s", c.Req.URL.RawQuery)
	defer logger.Info("EXIT KeyView.New")
	memberShip := GetMemberShip(c.Req.Context())
	permit := memberShip.CheckOrgPermission(model.OrgWriter)
	if !permit {
		logger.Error("Not authorized for this operation")
		c.Data["ErrorMsg"] = "Not authorized for this operation"
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	c.HTML(200, "keys_new")
}

func (v *KeyView) Confirm(c *macaron.Context, store session.Store) {
	logger.Infof("ENTER KeyView.Confirm: query=%s", c.Req.URL.RawQuery)
	defer logger.Info("EXIT KeyView.Confirm")
	ctx := c.Req.Context()
	name := c.QueryTrim("name")
	publicKey := c.QueryTrim("pubkey")
	_, err := keyAdmin.Create(ctx, name, publicKey, "")
	if err != nil {
		logger.Error("Failed to create key ", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(500, "500")
		return
	}
	if c.QueryTrim("from_instance") != "" {
		_, _, err := keyAdmin.List(c.Req.Context(), 0, -1, "", "")
		if err != nil {
			logger.Error("Failed to list keys ", err)
			c.Data["ErrorMsg"] = err.Error()
			c.HTML(500, "500")
			return
		}
	} else {
		var redirectTo string
		redirectTo = "../keys"
		c.Redirect(redirectTo)
	}
}

func (v *KeyView) SolvePrintedPublicKeyError(c *macaron.Context, store session.Store, err error) {
	if err != nil {
		if c.QueryTrim("from_instance") != "" {
			c.JSON(200, map[string]interface{}{
				"error": "Public key is wrong",
			})
			return
		} else {
			logger.Error("Public key is wrong")
			c.Data["ErrorMsg"] = "Public key is wrong"
			c.HTML(http.StatusBadRequest, "error")
			return
		}
	}
}

/*
func (v *KeyView) SolvePublicKeyDbError(c *macaron.Context, store session.Store, name, publicKey, fingerPrint string) {
	key, err := keyAdmin.Create(c.Req.Context(), name, publicKey, fingerPrint)
	if err != nil {
		logger.Error("Failed, %v", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(500, "500")
		return
	}
	return
}

func (v *KeyView) SearchDbFingerPrint(c *macaron.Context, store session.Store, fingerPrint, publicKey, name string) {
	db := DB()
	var keydb []model.Key
	x := db.Where(&model.Key{FingerPrint: fingerPrint}).Find(&keydb)
	length := len(*(x.Value.(*[]model.Key)))
	if length != 0 {
		if c.QueryTrim("from_instance") != "" {
			c.JSON(200, map[string]interface{}{
				"error": "This public key has been used",
			})
			return
		} else {
			c.Data["ErrorMsg"] = "This public key has been used"
			c.HTML(http.StatusBadRequest, "error")
			return
		}
	} else {
		keyView.SolvePublicKeyDbError(c, store, name, publicKey, fingerPrint)
	}
}
*/

func (v *KeyView) SolveListKeyError(c *macaron.Context, store session.Store) {
	if c.QueryTrim("from_instance") != "" {
		_, _, err := keyAdmin.List(c.Req.Context(), 0, -1, "", "")
		if err != nil {
			logger.Error("Failed to list keys, %v", err)
			c.Data["ErrorMsg"] = err.Error()
			c.HTML(500, "500")
			return
		}
	} else {
		redirectTo := "../keys"
		c.Redirect(redirectTo)
	}
}

func (v *KeyView) Create(c *macaron.Context, store session.Store) {
	logger.Infof("ENTER KeyView.Create: query=%s", c.Req.URL.RawQuery)
	defer logger.Info("EXIT KeyView.Create")
	memberShip := GetMemberShip(c.Req.Context())
	permit := memberShip.CheckOrgPermission(model.OrgWriter)
	if !permit {
		logger.Error("Not authorized for this operation")
		c.Data["ErrorMsg"] = "Not authorized for this operation"
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	ctx := c.Req.Context()
	name := c.QueryTrim("name")
	if c.QueryTrim("pubkey") != "" {
		publicKey := c.QueryTrim("pubkey")
		_, err := keyAdmin.Create(ctx, name, publicKey, "")
		if err != nil {
			logger.Error("failed to create key")
			c.Data["ErrorMsg"] = err.Error()
			c.HTML(http.StatusBadRequest, "error")
		}
		redirectTo := "../keys"
		c.Redirect(redirectTo)
	} else {
		publicKey, fingerPrint, privateKey, err := keyAdmin.CreateKeyPair(ctx)
		if err != nil {
			logger.Error("failed")
			c.Data["ErrorMsg"] = err.Error()
			c.HTML(http.StatusBadRequest, "error")
			return
		}
		if c.QueryTrim("from_instance") != "" {
			fmt.Println("from_instance:" + c.QueryTrim("from_instance"))
			c.JSON(200, map[string]interface{}{
				"keyName":    name,
				"publicKey":  publicKey,
				"privateKey": privateKey,
			})
			return
		} else {
			c.Data["KeyName"] = name
			c.Data["PublicKey"] = publicKey
			c.Data["PrivateKey"] = privateKey
			c.Data["fingerPrint"] = fingerPrint
			c.HTML(200, "new_key")
		}
	}
}
