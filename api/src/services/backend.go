/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

*/

package services

import (
	"context"
	"encoding/json"
	"fmt"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"
)

var (
	backendAdmin = &BackendAdmin{}
)

type BackendAdmin struct{}

func (a *BackendAdmin) CreateHaproxyConf(ctx context.Context, updatedlistener *model.Listener, loadBalancer *model.LoadBalancer) (err error) {
	logger.Infof("ENTER BackendAdmin.CreateHaproxyConf: loadBalancerID=%d", loadBalancer.ID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT BackendAdmin.CreateHaproxyConf: error=%v", err)
		} else {
			logger.Info("EXIT BackendAdmin.CreateHaproxyConf: success")
		}
	}()
	listeners := loadBalancer.Listeners
	listenerCfgs := []*ListenerConfig{}
	for _, listener := range listeners {
		if updatedlistener != nil && listener.ID == updatedlistener.ID {
			listener = updatedlistener
		}
		if len(listener.Backends) > 0 {
			backendCfgs := []*BackendConfig{}
			for _, backend := range listener.Backends {
				backendCfgs = append(backendCfgs, &BackendConfig{
					BackendURL: backend.BackendAddr,
					Status:     backend.Status,
				})
			}
			listenerCfgs = append(listenerCfgs, &ListenerConfig{
				Name:     fmt.Sprintf("lb-%d-lsn-%d", loadBalancer.ID, listener.ID),
				Mode:     listener.Mode,
				Key:      listener.Key,
				Cert:     listener.Certificate,
				Port:     listener.Port,
				Backends: backendCfgs,
			})
		}
	}
	haproxyCfg := &LoadBalancerConfig{Listeners: listenerCfgs}
	for _, fip := range loadBalancer.FloatingIps {
		haproxyCfg.FloatingIps = append(haproxyCfg.FloatingIps, fip.FipAddress)
	}
	jsonData, err := json.Marshal(haproxyCfg)
	if err != nil {
		logger.Errorf("Failed to marshal load balancer json data, %v", err)
		return
	}
	hyperGroup, _, _, err := GetVrrpHyperGroup(ctx, loadBalancer.VrrpInstance)
	if err != nil {
		logger.Errorf("Failed to get hyper group, %v", err)
		return
	}
	control := "toall=" + hyperGroup
	command := fmt.Sprintf("/opt/cloudland/scripts/backend/create_haproxy_conf.sh '%d' '%d' '%d'<<EOF\n%s\nEOF", loadBalancer.RouterID, loadBalancer.ID, loadBalancer.VrrpInstance.ID, jsonData)
	err = HyperExecute(ctx, control, command)
	if err != nil {
		logger.Error("create haproxy conf execution failed", err)
		return
	}
	return
}

func (a *BackendAdmin) Create(ctx context.Context, name, backendAddr string, ssl bool, listener *model.Listener, loadBalancer *model.LoadBalancer) (backend *model.Backend, err error) {
	logger.Infof("ENTER BackendAdmin.Create: name=%s, backendAddr=%s, listenerID=%d, loadBalancerID=%d", name, backendAddr, listener.ID, loadBalancer.ID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT BackendAdmin.Create: error=%v", err)
		} else {
			logger.Infof("EXIT BackendAdmin.Create: success, backendID=%d", backend.ID)
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckOrgPermission(model.OrgWriter)
	if !permit {
		logger.Error("Not authorized to create backend")
		err = NewCLError(ErrPermissionDenied, "Not authorized to create backend", nil)
		return
	}
	owner := memberShip.OrgID
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	backend = &model.Backend{Model: model.Model{Creater: memberShip.UserID}, Owner: owner, ListenerID: listener.ID, Name: name, BackendAddr: backendAddr, SSL: ssl, Status: "available"}
	err = db.Create(backend).Error
	if err != nil {
		logger.Error("DB failed to create backend ", err)
		err = NewCLError(ErrBackendCreateFailed, "Failed to create backend", err)
		return
	}
	listener.Backends = append(listener.Backends, backend)
	err = a.CreateHaproxyConf(ctx, listener, loadBalancer)
	if err != nil {
		logger.Error("Failed to create haproxy conf ", err)
		err = NewCLError(ErrBackendCreateFailed, "Failed to create haproxy conf", err)
		return
	}
	return
}

func (a *BackendAdmin) Get(ctx context.Context, id int64, listener *model.Listener) (backend *model.Backend, err error) {
	logger.Infof("ENTER BackendAdmin.Get: id=%d, listenerID=%d", id, listener.ID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT BackendAdmin.Get: error=%v", err)
		} else {
			logger.Infof("EXIT BackendAdmin.Get: success, backendUUID=%s", backend.UUID)
		}
	}()
	if id <= 0 {
		logger.Error("returning nil backend")
		return
	}
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	query, args := memberShip.GetOrgFilter()
	backend = &model.Backend{Model: model.Model{ID: id}}
	if err = db.Where(query, args...).Take(backend).Error; err != nil {
		logger.Error("Failed to query backend", err)
		err = NewCLError(ErrBackendNotFound, "Failed to find backend", err)
		return
	}
	permit := memberShip.CheckResourceOrg(model.OrgReader, backend.Owner)
	if !permit {
		logger.Error("Not authorized to read the backend")
		err = NewCLError(ErrPermissionDenied, "Not authorized to read the backend", nil)
		return
	}
	return
}

func (a *BackendAdmin) GetBackendByUUID(ctx context.Context, uuID string) (backend *model.Backend, err error) {
	logger.Infof("ENTER BackendAdmin.GetBackendByUUID: uuID=%s", uuID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT BackendAdmin.GetBackendByUUID: error=%v", err)
		} else {
			logger.Infof("EXIT BackendAdmin.GetBackendByUUID: success, id=%d", backend.ID)
		}
	}()
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	query, args := memberShip.GetOrgFilter()
	backend = &model.Backend{}
	err = db.Where(query, args...).Where("uuid = ?", uuID).Take(backend).Error
	if err != nil {
		logger.Error("Failed to query backend, %v", err)
		err = NewCLError(ErrRouterNotFound, "Failed to find backend", err)
		return
	}
	permit := memberShip.CheckResourceOrg(model.OrgReader, backend.Owner)
	if !permit {
		logger.Error("Not authorized to read the backend")
		err = NewCLError(ErrPermissionDenied, "Not authorized to read the backend", nil)
		return
	}
	return
}

func (a *BackendAdmin) GetBackendByName(ctx context.Context, name string) (backend *model.Backend, err error) {
	logger.Infof("ENTER BackendAdmin.GetBackendByName: name=%s", name)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT BackendAdmin.GetBackendByName: error=%v", err)
		} else {
			logger.Infof("EXIT BackendAdmin.GetBackendByName: success, id=%d", backend.ID)
		}
	}()
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	query, args := memberShip.GetOrgFilter()
	backend = &model.Backend{}
	err = db.Where(query, args...).Where("name = ?", name).Take(backend).Error
	if err != nil {
		logger.Error("Failed to query backend, %v", err)
		err = NewCLError(ErrRouterNotFound, "Failed to find backend", err)
		return
	}
	permit := memberShip.CheckResourceOrg(model.OrgReader, backend.Owner)
	if !permit {
		logger.Error("Not authorized to read the backend")
		err = NewCLError(ErrPermissionDenied, "Not authorized to read the backend", nil)
		return
	}
	return
}

func (a *BackendAdmin) GetBackend(ctx context.Context, reference *BaseReference) (backend *model.Backend, err error) {
	logger.Infof("ENTER BackendAdmin.GetBackend: reference=%v", reference)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT BackendAdmin.GetBackend: error=%v", err)
		} else {
			logger.Info("EXIT BackendAdmin.GetBackend: success")
		}
	}()
	if reference == nil || (reference.ID == "" && reference.Name == "") {
		err = NewCLError(ErrInvalidParameter, "Router base reference must be provided with either uuid or name", nil)
		return
	}
	if reference.ID != "" {
		backend, err = a.GetBackendByUUID(ctx, reference.ID)
		return
	}
	if reference.Name != "" {
		backend, err = a.GetBackendByName(ctx, reference.Name)
		return
	}
	return
}

func (a *BackendAdmin) Update(ctx context.Context, backend *model.Backend, backendAddr string) (lb *model.Backend, err error) {
	logger.Infof("ENTER BackendAdmin.Update: backendID=%d, backendAddr=%s", backend.ID, backendAddr)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT BackendAdmin.Update: error=%v", err)
		} else {
			logger.Info("EXIT BackendAdmin.Update: success")
		}
	}()
	ctx, db := GetContextDB(ctx)
	if backend.BackendAddr != backendAddr {
		backend.BackendAddr = backendAddr
		if err = db.Model(backend).Update("backend_addr", backend.BackendAddr).Error; err != nil {
			logger.Error("Failed to save backend", err)
			err = NewCLError(ErrRouterUpdateFailed, "Failed to update backend", err)
			return
		}
	}
	lb = backend
	return
}

func (a *BackendAdmin) Delete(ctx context.Context, backend *model.Backend, listener *model.Listener, loadBalancer *model.LoadBalancer) (err error) {
	logger.Infof("ENTER BackendAdmin.Delete: backendID=%d, listenerID=%d, loadBalancerID=%d", backend.ID, listener.ID, loadBalancer.ID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT BackendAdmin.Delete: error=%v", err)
		} else {
			logger.Info("EXIT BackendAdmin.Delete: success")
		}
	}()
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckResourceOrg(model.OrgWriter, backend.Owner)
	if !permit {
		logger.Error("Not authorized to delete the backend")
		err = NewCLError(ErrPermissionDenied, "Not authorized to delete the router", nil)
		return
	}
	backend.BackendAddr = fmt.Sprintf("%s-%d", backend.BackendAddr, backend.CreatedAt.UnixNano())
	err = db.Model(backend).Update("backend_addr", backend.BackendAddr).Error
	if err != nil {
		logger.Error("DB failed to update backend address", err)
		err = NewCLError(ErrSubnetUpdateFailed, "DB failed to update backend address", err)
		return
	}
	if err = db.Delete(backend).Error; err != nil {
		logger.Error("DB failed to delete backend", err)
		err = NewCLError(ErrRouterDeleteFailed, "Failed to delete backend", err)
		return
	}
	_, backends, err := backendAdmin.List(ctx, 0, -1, "", listener)
	if err != nil {
		logger.Error("DB failed to count backends, %v", err)
		err = NewCLError(ErrSQLSyntaxError, "Failed to count backends", err)
		return
	}
	listener.Backends = backends
	err = a.CreateHaproxyConf(ctx, listener, loadBalancer)
	if err != nil {
		logger.Error("Failed to delete haproxy conf ", err)
		err = NewCLError(ErrBackendDeleteFailed, "Failed to delete haproxy conf", err)
		return
	}
	return
}

func (a *BackendAdmin) List(ctx context.Context, offset, limit int64, order string, listener *model.Listener) (total int64, backends []*model.Backend, err error) {
	logger.Infof("ENTER BackendAdmin.List: offset=%d, limit=%d, order=%s, listenerID=%d", offset, limit, order, listener.ID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT BackendAdmin.List: error=%v", err)
		} else {
			logger.Infof("EXIT BackendAdmin.List: total=%d, count=%d", total, len(backends))
		}
	}()
	memberShip := GetMemberShip(ctx)
	ctx, db := GetContextDB(ctx)
	if limit == 0 {
		limit = 16
	}

	if order == "" {
		order = "created_at"
	}
	where := fmt.Sprintf("listener_id = %d", listener.ID)
	query, args := memberShip.GetOrgFilter()
	backends = []*model.Backend{}
	if err = db.Model(&model.Backend{}).Where(where).Where(query, args...).Count(&total).Error; err != nil {
		logger.Error("DB failed to count backends, %v", err)
		err = NewCLError(ErrSQLSyntaxError, "Failed to count backends", err)
		return
	}
	db = dbs.Sortby(db.Offset(offset).Limit(limit), order)
	if err = db.Where(where).Where(query, args...).Find(&backends).Error; err != nil {
		logger.Error("DB failed to query backends, %v", err)
		err = NewCLError(ErrSQLSyntaxError, "Failed to query backends", err)
		return
	}
	permit := memberShip.CheckSystemPermission()
	if permit {
		_ = permit // SystemAdmin can see all backends
	}
	return
}
