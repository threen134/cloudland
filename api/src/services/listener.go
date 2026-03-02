/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

*/

package services

import (
	"context"
	"encoding/base64"
	"fmt"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"
)

var (
	listenerAdmin = &ListenerAdmin{}
)

type ListenerAdmin struct{}

func (a *ListenerAdmin) Create(ctx context.Context, name, mode, key, cert string, port int32, loadBalancer *model.LoadBalancer) (listener *model.Listener, err error) {
	logger.Infof("ENTER ListenerAdmin.Create: name=%s, mode=%s, port=%d, lbID=%d", name, mode, port, loadBalancer.ID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT ListenerAdmin.Create: error=%v", err)
		} else {
			logger.Infof("EXIT ListenerAdmin.Create: success, listenerID=%d", listener.ID)
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckOrgPermission(model.OrgWriter)
	if !permit {
		logger.Error("Not authorized to create listener")
		err = NewCLError(ErrPermissionDenied, "Not authorized to create listener", nil)
		return
	}
	owner := memberShip.OrgID
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	listener = &model.Listener{Model: model.Model{Creater: memberShip.UserID}, Owner: owner, Name: name, Mode: mode, Key: base64.StdEncoding.EncodeToString([]byte(key)), Certificate: base64.StdEncoding.EncodeToString([]byte(cert)), Port: port, LoadBalancerID: loadBalancer.ID, Status: "available"}
	err = db.Create(listener).Error
	if err != nil {
		logger.Error("DB failed to create listener ", err)
		err = NewCLError(ErrListenerCreateFailed, "Failed to create listener", err)
		return
	}
	loadBalancer.Listeners = append(loadBalancer.Listeners, listener)
	err = CreateVrrpConf(ctx, loadBalancer)
	if err != nil {
		logger.Error("Recreate keepalived config failed", err)
		err = NewCLError(ErrVrrpInstanceCreateFailed, "Recreate keepalived config failed", err)
		return
	}
	return
}

func (a *ListenerAdmin) Get(ctx context.Context, id int64, loadBalancer *model.LoadBalancer) (listener *model.Listener, err error) {
	logger.Infof("ENTER ListenerAdmin.Get: id=%d, lbID=%d", id, loadBalancer.ID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT ListenerAdmin.Get: error=%v", err)
		} else {
			logger.Info("EXIT ListenerAdmin.Get: success")
		}
	}()
	if id <= 0 {
		logger.Error("returning nil listener")
		return
	}
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	where, args := memberShip.GetOrgFilter()
	listener = &model.Listener{Model: model.Model{ID: id}}
	if err = db.Preload("Backends").Where(where, args...).Where("load_balancer_id = ?", loadBalancer.ID).Take(listener).Error; err != nil {
		logger.Error("DB failed to query listener", err)
		return nil, NewCLError(ErrListenerNotFound, "Failed to find listener", err)
	}
	permit := memberShip.CheckResourceOrg(model.OrgReader, listener.Owner)
	if !permit {
		logger.Error("Not authorized to read the listener")
		err = NewCLError(ErrPermissionDenied, "Not authorized to read the listener", nil)
		return
	}
	return
}

func (a *ListenerAdmin) GetListenerByUUID(ctx context.Context, uuID string) (listener *model.Listener, err error) {
	logger.Infof("ENTER ListenerAdmin.GetListenerByUUID: uuID=%s", uuID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT ListenerAdmin.GetListenerByUUID: error=%v", err)
		} else {
			logger.Infof("EXIT ListenerAdmin.GetListenerByUUID: success, listenerID=%d", listener.ID)
		}
	}()
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	where, args := memberShip.GetOrgFilter()
	listener = &model.Listener{}
	if err = db.Preload("Backends").Where(where, args...).Where("uuid = ?", uuID).Take(listener).Error; err != nil {
		logger.Error("DB failed to query listener", err)
		return nil, NewCLError(ErrListenerNotFound, "Failed to find listener", err)
	}
	permit := memberShip.CheckResourceOrg(model.OrgReader, listener.Owner)
	if !permit {
		logger.Error("Not authorized to read the listener")
		err = NewCLError(ErrPermissionDenied, "Not authorized to read the listener", nil)
		return
	}
	return
}

func (a *ListenerAdmin) GetListenerByName(ctx context.Context, name string) (listener *model.Listener, err error) {
	logger.Infof("ENTER ListenerAdmin.GetListenerByName: name=%s", name)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT ListenerAdmin.GetListenerByName: error=%v", err)
		} else {
			logger.Infof("EXIT ListenerAdmin.GetListenerByName: success, listenerID=%d", listener.ID)
		}
	}()
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	where, args := memberShip.GetOrgFilter()
	listener = &model.Listener{}
	if err = db.Preload("Backends").Where(where, args...).Where("name = ?", name).Take(listener).Error; err != nil {
		logger.Error("DB failed to query listener", err)
		return nil, NewCLError(ErrListenerNotFound, "Failed to find listener", err)
	}
	permit := memberShip.CheckResourceOrg(model.OrgReader, listener.Owner)
	if !permit {
		logger.Error("Not authorized to read the listener")
		err = NewCLError(ErrPermissionDenied, "Not authorized to read the listener", nil)
		return
	}
	return
}

func (a *ListenerAdmin) GetListener(ctx context.Context, reference *BaseReference) (listener *model.Listener, err error) {
	logger.Infof("ENTER ListenerAdmin.GetListener: reference=%+v", reference)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT ListenerAdmin.GetListener: error=%v", err)
		} else {
			logger.Info("EXIT ListenerAdmin.GetListener: success")
		}
	}()
	if reference == nil || (reference.ID == "" && reference.Name == "") {
		err = NewCLError(ErrInvalidParameter, "Router base reference must be provided with either uuid or name", nil)
		return
	}
	if reference.ID != "" {
		listener, err = a.GetListenerByUUID(ctx, reference.ID)
		return
	}
	if reference.Name != "" {
		listener, err = a.GetListenerByName(ctx, reference.Name)
		return
	}
	return
}

func (a *ListenerAdmin) Update(ctx context.Context, listener *model.Listener, name string) (lb *model.Listener, err error) {
	logger.Infof("ENTER ListenerAdmin.Update: listenerID=%d, name=%s", listener.ID, name)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT ListenerAdmin.Update: error=%v", err)
		} else {
			logger.Info("EXIT ListenerAdmin.Update: success")
		}
	}()
	ctx, db := GetContextDB(ctx)
	if listener.Name != name {
		listener.Name = name
		if err = db.Model(listener).Update("name", listener.Name).Error; err != nil {
			logger.Error("Failed to save listener", err)
			err = NewCLError(ErrRouterUpdateFailed, "Failed to update listener", err)
			return
		}
	}
	lb = listener
	return
}

func (a *ListenerAdmin) Delete(ctx context.Context, listener *model.Listener, loadBalancer *model.LoadBalancer) (err error) {
	logger.Infof("ENTER ListenerAdmin.Delete: listenerID=%d, lbID=%d", listener.ID, loadBalancer.ID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT ListenerAdmin.Delete: error=%v", err)
		} else {
			logger.Info("EXIT ListenerAdmin.Delete: success")
		}
	}()
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckResourceOrg(model.OrgReader, listener.Owner)
	if !permit {
		logger.Error("Not authorized to delete the listener")
		err = NewCLError(ErrPermissionDenied, "Not authorized to delete the listener", nil)
		return
	}
	_, backends, err := backendAdmin.List(ctx, 0, -1, "", listener)
	if err != nil {
		logger.Error("Failed to list backends", err)
		err = NewCLError(ErrBackendListFailed, "Failed to list backends", err)
		return
	}
	for _, backend := range backends {
		err = backendAdmin.Delete(ctx, backend, listener, loadBalancer)
		if err != nil {
			logger.Error("Failed to delete backend", err)
			err = NewCLError(ErrBackendListFailed, "Failed to delete backend", err)
			return
		}
	}
	if err = db.Delete(listener).Error; err != nil {
		logger.Error("DB failed to delete listener", err)
		err = NewCLError(ErrListenerDeleteFailed, "Failed to delete listener", err)
		return
	}
	listener.Name = fmt.Sprintf("%s-%d", listener.Name, listener.CreatedAt.Unix())
	err = db.Model(&model.Listener{}).Unscoped().Where("id = ?", listener.ID).Update("name", listener.Name).Error
	if err != nil {
		logger.Error("DB failed to update listsner name", err)
		err = NewCLError(ErrListenerUpdateFailed, "DB failed to update listener name", err)
		return
	}
	_, listeners, err := listenerAdmin.List(ctx, 0, -1, "", loadBalancer)
	if err != nil {
		logger.Error("DB failed to count listeners, %v", err)
		err = NewCLError(ErrSQLSyntaxError, "Failed to count listeners", err)
		return
	}
	loadBalancer.Listeners = listeners
	err = CreateVrrpConf(ctx, loadBalancer)
	if err != nil {
		logger.Error("Recreate keepalived config failed", err)
		err = NewCLError(ErrVrrpInstanceCreateFailed, "Recreate keepalived config failed", err)
		return
	}
	return
}

func (a *ListenerAdmin) List(ctx context.Context, offset, limit int64, order string, loadBalancer *model.LoadBalancer) (total int64, listeners []*model.Listener, err error) {
	logger.Infof("ENTER ListenerAdmin.List: lbID=%d, offset=%d, limit=%d, order=%s", loadBalancer.ID, offset, limit, order)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT ListenerAdmin.List: error=%v", err)
		} else {
			logger.Infof("EXIT ListenerAdmin.List: total=%d, count=%d", total, len(listeners))
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
	queryBuilder, args := memberShip.GetOrgFilter()
	listeners = []*model.Listener{}
	if err = db.Model(&model.Listener{}).Where(queryBuilder, args...).Where("load_balancer_id = ?", loadBalancer.ID).Count(&total).Error; err != nil {
		logger.Error("DB failed to count load balancer, %v", err)
		err = NewCLError(ErrSQLSyntaxError, "Failed to count load balancer", err)
		return
	}
	db = dbs.Sortby(db.Offset(offset).Limit(limit), order)
	if err = db.Preload("Backends").Where(queryBuilder, args...).Where("load_balancer_id = ?", loadBalancer.ID).Find(&listeners).Error; err != nil {
		logger.Error("DB failed to query listeners, %v", err)
		err = NewCLError(ErrSQLSyntaxError, "Failed to query listeners", err)
		return
	}
	permit = memberShip.IsSystemAdmin()
	if permit {
		db = db.Offset(0).Limit(-1)
		for _, listener := range listeners {
			listener.OwnerInfo = &model.Organization{Model: model.Model{ID: listener.Owner}}
			if err = db.Take(listener.OwnerInfo).Error; err != nil {
				logger.Error("Failed to query owner info", err)
				err = NewCLError(ErrOwnerNotFound, "Failed to query owner info", err)
				return
			}
		}
	}
	return
}
