/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

*/

package routes

import (
	"context"
	"encoding/base64"
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
	listenerAdmin = &ListenerAdmin{}
	listenerView  = &ListenerView{}
)

type ListenerAdmin struct{}
type ListenerView struct{}

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
	listener.Name = fmt.Sprintf("%s-%d", listener.Name, listener.CreatedAt.Unix())
	err = db.Model(&model.Listener{Model: model.Model{ID: listener.ID}}).Update("name", listener.Name).Error
	if err != nil {
		logger.Error("DB failed to update listsner name", err)
		err = NewCLError(ErrListenerUpdateFailed, "DB failed to update listener name", err)
		return
	}
	if err = db.Delete(listener).Error; err != nil {
		logger.Error("DB failed to delete listener", err)
		err = NewCLError(ErrListenerDeleteFailed, "Failed to delete listener", err)
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

func (v *ListenerView) List(c *macaron.Context, store session.Store) {
	logger.Infof("ENTER ListenerView.List: params=%v, query=%s", c.Params, c.Req.URL.RawQuery)
	defer logger.Info("EXIT ListenerView.List")
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

	total, listeners, err := listenerAdmin.List(c.Req.Context(), offset, limit, order, loadBalancer)
	if err != nil {
		logger.Error("Failed to list listeners, %v", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(500, "500")
		return
	}
	pages := GetPages(total, limit)
	c.Data["Listeners"] = listeners
	c.Data["Total"] = total
	c.Data["Pages"] = pages
	c.HTML(200, "listeners")
}

func (v *ListenerView) Delete(c *macaron.Context, store session.Store) (err error) {
	logger.Infof("ENTER ListenerView.Delete: params=%v", c.Params)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT ListenerView.Delete: error=%v", err)
		} else {
			logger.Info("EXIT ListenerView.Delete: success")
		}
	}()
	ctx := c.Req.Context()
	id := c.Params("id")
	if id == "" {
		logger.Error("Id is empty")
		c.Data["ErrorMsg"] = "Id is empty"
		c.Error(http.StatusBadRequest)
		return
	}
	listenerID, err := strconv.Atoi(id)
	if err != nil {
		logger.Error("Invalid listener id, %v", err)
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
	listener, err := listenerAdmin.Get(ctx, int64(listenerID), loadBalancer)
	if err != nil {
		logger.Error("Not able to get listener")
		c.Data["ErrorMsg"] = err.Error()
		c.Error(http.StatusBadRequest)
		return
	}
	err = listenerAdmin.Delete(ctx, listener, loadBalancer)
	if err != nil {
		logger.Error("Failed to delete listener, %v", err)
		c.Data["ErrorMsg"] = err.Error()
		c.Error(http.StatusBadRequest)
		return
	}
	c.JSON(200, map[string]interface{}{
		"redirect": "listeners",
	})
	return
}

func (v *ListenerView) New(c *macaron.Context, store session.Store) {
	logger.Infof("ENTER ListenerView.New: params=%v", c.Params)
	defer logger.Info("EXIT ListenerView.New")
	memberShip := GetMemberShip(c.Req.Context())
	permit := memberShip.CheckOrgPermission(model.OrgWriter)
	if !permit {
		logger.Error("Not authorized for this operation")
		c.Data["ErrorMsg"] = "Not authorized for this operation"
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	c.HTML(200, "listeners_new")
}

func (v *ListenerView) Edit(c *macaron.Context, store session.Store) {
	logger.Infof("ENTER ListenerView.Edit: params=%v", c.Params)
	defer logger.Info("EXIT ListenerView.Edit")
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
	id := c.Params("id")
	listenerID, err := strconv.Atoi(id)
	if err != nil {
		logger.Error("Invalid listener id, %v", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	listener, err := listenerAdmin.Get(ctx, int64(listenerID), loadBalancer)
	if err != nil {
		logger.Error("Failed to get listener, %v", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	c.Data["Listener"] = listener
	c.HTML(200, "listeners_patch")
}

func (v *ListenerView) Patch(c *macaron.Context, store session.Store) {
	logger.Infof("ENTER ListenerView.Patch: params=%v, query=%s", c.Params, c.Req.URL.RawQuery)
	defer logger.Info("EXIT ListenerView.Patch")
	ctx := c.Req.Context()
	redirectTo := "../listeners"
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
	id := c.Params("id")
	listenerID, err := strconv.Atoi(id)
	if err != nil {
		logger.Error("Invalid listener id, %v", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	listener, err := listenerAdmin.Get(ctx, int64(listenerID), loadBalancer)
	if err != nil {
		logger.Error("Failed to get listener, %v", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	name := c.QueryTrim("name")
	_, err = listenerAdmin.Update(ctx, listener, name)
	if err != nil {
		logger.Error("Failed to update listener", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	c.Redirect(redirectTo)
}

func (v *ListenerView) Create(c *macaron.Context, store session.Store) {
	logger.Infof("ENTER ListenerView.Create: params=%v, query=%s", c.Params, c.Req.URL.RawQuery)
	defer logger.Info("EXIT ListenerView.Create")
	ctx := c.Req.Context()
	redirectTo := "../listeners"
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
	name := c.QueryTrim("name")
	mode := c.QueryTrim("mode")
	if mode == "" {
		mode = "http"
	}
	key := c.QueryTrim("key")
	cert := c.QueryTrim("cert")
	port := c.QueryInt("port")
	if port <= 0 {
		logger.Errorf("Invalid port %d", port)
		c.Data["ErrorMsg"] = "Invalid port"
		c.HTML(404, "404")
		return
	}
	_, err = listenerAdmin.Create(ctx, name, mode, key, cert, int32(port), loadBalancer)
	if err != nil {
		logger.Error("Failed to create listener, %v", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	c.Redirect(redirectTo)
}
