/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

*/

package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	. "web/src/common"
	"web/src/dbs"
	"web/src/model"

	"github.com/go-macaron/session"
	macaron "gopkg.in/macaron.v1"
)

var (
	backendAdmin = &BackendAdmin{}
	backendView  = &BackendView{}
)

type BackendAdmin struct{}
type BackendView struct{}

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

func (a *BackendAdmin) Create(ctx context.Context, name, backendAddr string, listener *model.Listener, loadBalancer *model.LoadBalancer) (backend *model.Backend, err error) {
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
	backend = &model.Backend{Model: model.Model{Creater: memberShip.UserID}, Owner: owner, ListenerID: listener.ID, Name: name, BackendAddr: backendAddr, Status: "available"}
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

func (v *BackendView) List(c *macaron.Context, store session.Store) {
	logger.Infof("ENTER BackendView.List: params=%v, query=%s", c.Params, c.Req.URL.RawQuery)
	defer logger.Info("EXIT BackendView.List")
	ctx := c.Req.Context()
	offset := c.QueryInt64("offset")
	limit := c.QueryInt64("limit")
	if limit == 0 {
		limit = 16
	}
	order := c.QueryTrim("order")
	if order == "" {
		order = "-created_at"
	}
	lbid := c.Params("lbid")
	if lbid == "" {
		logger.Error("Load balancer ID is empty")
		c.Data["ErrorMsg"] = "Load balancer ID is empty"
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	loadBalancerID, err := strconv.Atoi(lbid)
	if err != nil {
		logger.Error("Invalid load balancer ID", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	loadBalancer, err := loadBalancerAdmin.Get(ctx, int64(loadBalancerID))
	if err != nil {
		logger.Error("Failed to get load balancer", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	lstnid := c.Params("lstnid")
	if lstnid == "" {
		logger.Error("Listener ID is empty")
		c.Data["ErrorMsg"] = "Listenr ID is empty"
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	listenerID, err := strconv.Atoi(lstnid)
	if err != nil {
		logger.Error("Invalid listener ID", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	listener, err := listenerAdmin.Get(ctx, int64(listenerID), loadBalancer)
	if err != nil {
		logger.Error("Failed to get listener", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(http.StatusBadRequest, "error")
		return
	}

	total, backends, err := backendAdmin.List(c.Req.Context(), offset, limit, order, listener)
	if err != nil {
		logger.Error("Failed to list backends, %v", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(500, "500")
		return
	}
	pages := GetPages(total, limit)
	c.Data["Backends"] = backends
	c.Data["Total"] = total
	c.Data["Pages"] = pages
	c.HTML(200, "backends")
}

func (v *BackendView) Delete(c *macaron.Context, store session.Store) (err error) {
	logger.Infof("ENTER BackendView.Delete: params=%v", c.Params)
	defer logger.Info("EXIT BackendView.Delete")
	ctx := c.Req.Context()
	id := c.Params("id")
	if id == "" {
		logger.Error("Id is empty")
		c.Data["ErrorMsg"] = "Id is empty"
		c.Error(http.StatusBadRequest)
		return
	}
	backendID, err := strconv.Atoi(id)
	if err != nil {
		logger.Error("Invalid backend id, %v", err)
		c.Data["ErrorMsg"] = err.Error()
		c.Error(http.StatusBadRequest)
		return
	}
	lbid := c.Params("lbid")
	if lbid == "" {
		logger.Error("Load balancer ID is empty")
		c.Data["ErrorMsg"] = "Load balancer ID is empty"
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	loadBalancerID, err := strconv.Atoi(lbid)
	if err != nil {
		logger.Error("Invalid load balancer ID", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	loadBalancer, err := loadBalancerAdmin.Get(ctx, int64(loadBalancerID))
	if err != nil {
		logger.Error("Failed to get load balancer", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	lstnid := c.Params("lstnid")
	if lstnid == "" {
		logger.Error("Listener ID is empty")
		c.Data["ErrorMsg"] = "Listenr ID is empty"
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	listenerID, err := strconv.Atoi(lstnid)
	if err != nil {
		logger.Error("Invalid listener ID", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	listener, err := listenerAdmin.Get(ctx, int64(listenerID), loadBalancer)
	if err != nil {
		logger.Error("Failed to get listener", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	backend, err := backendAdmin.Get(ctx, int64(backendID), listener)
	if err != nil {
		logger.Error("Not able to get backend")
		c.Data["ErrorMsg"] = err.Error()
		c.Error(http.StatusBadRequest)
		return
	}
	err = backendAdmin.Delete(ctx, backend, listener, loadBalancer)
	if err != nil {
		logger.Error("Failed to delete backend, %v", err)
		c.Data["ErrorMsg"] = err.Error()
		c.Error(http.StatusBadRequest)
		return
	}
	c.JSON(200, map[string]interface{}{
		"redirect": "backends",
	})
	return
}

func (v *BackendView) New(c *macaron.Context, store session.Store) {
	logger.Infof("ENTER BackendView.New: params=%v", c.Params)
	defer logger.Info("EXIT BackendView.New")
	ctx := c.Req.Context()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckOrgPermission(model.OrgWriter)
	if !permit {
		logger.Error("Not authorized for this operation")
		c.Data["ErrorMsg"] = "Not authorized for this operation"
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	c.HTML(200, "backends_new")
}

func (v *BackendView) Edit(c *macaron.Context, store session.Store) {
	logger.Infof("ENTER BackendView.Edit: params=%v", c.Params)
	defer logger.Info("EXIT BackendView.Edit")
	ctx := c.Req.Context()
	lbid := c.Params("lbid")
	if lbid == "" {
		logger.Error("Load balancer ID is empty")
		c.Data["ErrorMsg"] = "Load balancer ID is empty"
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	loadBalancerID, err := strconv.Atoi(lbid)
	if err != nil {
		logger.Error("Invalid load balancer ID", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	loadBalancer, err := loadBalancerAdmin.Get(ctx, int64(loadBalancerID))
	if err != nil {
		logger.Error("Failed to get load balancer", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	lstnid := c.Params("lstnid")
	if lstnid == "" {
		logger.Error("Listener ID is empty")
		c.Data["ErrorMsg"] = "Listenr ID is empty"
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	listenerID, err := strconv.Atoi(lstnid)
	if err != nil {
		logger.Error("Invalid listener ID", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	listener, err := listenerAdmin.Get(ctx, int64(listenerID), loadBalancer)
	if err != nil {
		logger.Error("Failed to get listener", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	id := c.Params("id")
	backendID, err := strconv.Atoi(id)
	if err != nil {
		logger.Error("Invalid backend id, %v", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	backend, err := backendAdmin.Get(ctx, int64(backendID), listener)
	if err != nil {
		logger.Error("Failed to get backend, %v", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	c.Data["Backend"] = backend
	c.HTML(200, "backends_patch")
}

func (v *BackendView) Patch(c *macaron.Context, store session.Store) {
	logger.Infof("ENTER BackendView.Patch: params=%v, query=%s", c.Params, c.Req.URL.RawQuery)
	defer logger.Info("EXIT BackendView.Patch")
	ctx := c.Req.Context()
	redirectTo := "../backends"
	lbid := c.Params("lbid")
	if lbid == "" {
		logger.Error("Load balancer ID is empty")
		c.Data["ErrorMsg"] = "Load balancer ID is empty"
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	loadBalancerID, err := strconv.Atoi(lbid)
	if err != nil {
		logger.Error("Invalid load balancer ID", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	loadBalancer, err := loadBalancerAdmin.Get(ctx, int64(loadBalancerID))
	if err != nil {
		logger.Error("Failed to get load balancer", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	lstnid := c.Params("lstnid")
	if lstnid == "" {
		logger.Error("Listener ID is empty")
		c.Data["ErrorMsg"] = "Listenr ID is empty"
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	listenerID, err := strconv.Atoi(lbid)
	if err != nil {
		logger.Error("Invalid listener ID", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	listener, err := listenerAdmin.Get(ctx, int64(listenerID), loadBalancer)
	if err != nil {
		logger.Error("Failed to get listener", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	id := c.Params("id")
	backendID, err := strconv.Atoi(id)
	if err != nil {
		logger.Error("Invalid backend id, %v", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	backend, err := backendAdmin.Get(ctx, int64(backendID), listener)
	if err != nil {
		logger.Error("Failed to get backend, %v", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	name := c.QueryTrim("name")
	_, err = backendAdmin.Update(ctx, backend, name)
	if err != nil {
		logger.Error("Failed to update backend", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	c.Redirect(redirectTo)
}

func (v *BackendView) Create(c *macaron.Context, store session.Store) {
	logger.Infof("ENTER BackendView.Create: params=%v, query=%s", c.Params, c.Req.URL.RawQuery)
	defer logger.Info("EXIT BackendView.Create")
	ctx := c.Req.Context()
	redirectTo := "../backends"
	lbid := c.Params("lbid")
	if lbid == "" {
		logger.Error("Load balancer ID is empty")
		c.Data["ErrorMsg"] = "Load balancer ID is empty"
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	loadBalancerID, err := strconv.Atoi(lbid)
	if err != nil {
		logger.Error("Invalid load balancer ID", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	loadBalancer, err := loadBalancerAdmin.Get(ctx, int64(loadBalancerID))
	if err != nil {
		logger.Error("Failed to get load balancer", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	lstnid := c.Params("lstnid")
	if lstnid == "" {
		logger.Error("Listener ID is empty")
		c.Data["ErrorMsg"] = "Listenr ID is empty"
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	listenerID, err := strconv.Atoi(lstnid)
	if err != nil {
		logger.Error("Invalid listener ID", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	listener, err := listenerAdmin.Get(ctx, int64(listenerID), loadBalancer)
	if err != nil {
		logger.Error("Failed to get listener", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	name := c.QueryTrim("name")
	backendAddr := c.QueryTrim("backend_addr")
	_, err = backendAdmin.Create(ctx, name, backendAddr, listener, loadBalancer)
	if err != nil {
		logger.Error("Failed to create backend, %v", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	c.Redirect(redirectTo)
}
