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
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	. "web/src/common"
	"web/src/dbs"
	"web/src/model"

	"github.com/go-macaron/session"
	"github.com/jinzhu/gorm"
	macaron "gopkg.in/macaron.v1"
)

var (
	ipGroupAdmin = &IpGroupAdmin{}
	ipGroupView  = &IpGroupView{}
)

type IpGroupAdmin struct{}

type IpGroupView struct{}

func (a *IpGroupAdmin) Create(ctx context.Context, name string, typeName string, ipGroupType int) (ipGroup *model.IpGroup, err error) {
	logger.Infof("ENTER IpGroupAdmin.Create: name=%s, typeName=%s, ipGroupType=%d", name, typeName, ipGroupType)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT IpGroupAdmin.Create: error=%v", err)
		} else {
			logger.Infof("EXIT IpGroupAdmin.Create: success, ipGroupID=%d", ipGroup.ID)
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckOrgPermission(model.OrgWriter)
	if !permit {
		logger.Error("Not authorized for this operation")
		err = NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
		return
	}
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	ipGroup = &model.IpGroup{
		Model:  model.Model{Creater: memberShip.UserID},
		Owner:  memberShip.OrgID,
		Name:   name,
		Type:   typeName,
		TypeID: int64(ipGroupType),
	}
	err = db.Create(ipGroup).Error
	if err != nil {
		logger.Error("Failed to create ipGroup", err)
		err = NewCLError(ErrIpGroupCreateFailed, "Failed to create ipGroup", err)
		return
	}
	err = db.Preload("DictionaryType").Preload("Subnets").Where("id = ?", ipGroup.ID).First(&ipGroup).Error
	if err != nil {
		logger.Error("Error loading IpGroup details after creation:", err)
		err = NewCLError(ErrIpGroupCreateFailed, "Error loading IpGroup details after creation", err)
		return nil, err
	}
	logger.Infof("IpGroupAdmin.Create: success, ipGroup=%+v", ipGroup)
	return ipGroup, nil
}

func (a *IpGroupAdmin) Get(ctx context.Context, id int64) (ipGroup *model.IpGroup, err error) {
	logger.Infof("ENTER IpGroupAdmin.Get: id=%d", id)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT IpGroupAdmin.Get: error=%v", err)
		} else {
			logger.Info("EXIT IpGroupAdmin.Get: success")
		}
	}()
	if id <= 0 {
		err = NewCLError(ErrInvalidParameter, fmt.Sprintf("Invalid ipGroup ID: %d", id), nil)
		logger.Errorf("%v", err)
		return
	}
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	query, args := memberShip.GetOrgFilter()
	ipGroup = &model.IpGroup{Model: model.Model{ID: id}}
	err = db.Where(query, args...).Preload("Subnets").Preload("DictionaryType").Take(ipGroup).Error
	if err != nil {
		logger.Errorf("Failed to query ipGroup, %v", err)
		err = NewCLError(ErrIpGroupNotFound, "IpGroup not found", err)
		return
	}
	logger.Infof("IpGroupAdmin.Get: success, ipGroup=%+v", ipGroup)
	return
}

func (a *IpGroupAdmin) Delete(ctx context.Context, ipGroup *model.IpGroup) (err error) {
	logger.Infof("ENTER IpGroupAdmin.Delete: id=%d", ipGroup.ID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT IpGroupAdmin.Delete: error=%v", err)
		} else {
			logger.Info("EXIT IpGroupAdmin.Delete: success")
		}
	}()
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckResourceOrg(model.OrgWriter, ipGroup.Owner)
	if !permit {
		logger.Error("Not authorized to delete the ip group")
		err = NewCLError(ErrPermissionDenied, "Not authorized to delete the ip group", nil)
		return
	}
	if err = db.Delete(ipGroup).Error; err != nil {
		logger.Errorf("DB failed to delete ip group, err=%v", err)
		err = NewCLError(ErrIpGroupDeleteFailed, "Failed to delete ip group", err)
		return
	}
	// Unscoped update
	ipGroup.Name = fmt.Sprintf("%s-%d", ipGroup.Name, ipGroup.CreatedAt.Unix())
	err = db.Model(&model.IpGroup{}).Unscoped().
		Where("id = ?", ipGroup.ID).
		Update("name", ipGroup.Name).Error
	if err != nil {
		logger.Error("DB failed to update ip group name", err)
		err = NewCLError(ErrIpGroupUpdateFailed, "Failed to update ip group name", err)
		return
	}
	return
}

func (a *IpGroupAdmin) GetIpGroupByUUID(ctx context.Context, uuID string) (ipGroup *model.IpGroup, err error) {
	logger.Infof("ENTER IpGroupAdmin.GetIpGroupByUUID: uuID=%s", uuID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT IpGroupAdmin.GetIpGroupByUUID: error=%v", err)
		} else {
			logger.Infof("EXIT IpGroupAdmin.GetIpGroupByUUID: success, id=%d", ipGroup.ID)
		}
	}()
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	query, args := memberShip.GetOrgFilter()
	ipGroup = &model.IpGroup{}
	err = db.Where(query, args...).Where("uuid = ?", uuID).Preload("Subnets").Preload("FloatingIPs", func(db *gorm.DB) *gorm.DB {
		return db.Order("floating_ips.updated_at")
	}).Preload("FloatingIPs.Subnet").Preload("FloatingIPs.Subnet.Group").Preload("FloatingIPs.Subnet.Group.DictionaryType").Preload("FloatingIPs.Group").Preload("FloatingIPs.Group.DictionaryType").Preload("DictionaryType").Take(ipGroup).Error
	if err != nil {
		logger.Errorf("Failed to query ipGroup, %v", err)
		err = NewCLError(ErrIpGroupNotFound, "IpGroup not found", err)
		return
	}
	return
}

func (a *IpGroupAdmin) GetIpGroupByName(ctx context.Context, name string) (ipGroup *model.IpGroup, err error) {
	logger.Infof("ENTER IpGroupAdmin.GetIpGroupByName: name=%s", name)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT IpGroupAdmin.GetIpGroupByName: error=%v", err)
		} else {
			logger.Infof("EXIT IpGroupAdmin.GetIpGroupByName: success, id=%d", ipGroup.ID)
		}
	}()
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	query, args := memberShip.GetOrgFilter()
	ipGroup = &model.IpGroup{}
	err = db.Where(query, args...).Where("name = ?", name).Preload("Subnets").Preload("DictionaryType").Take(ipGroup).Error
	if err != nil {
		logger.Errorf("Failed to query ipGroup, %v", err)
		err = NewCLError(ErrIpGroupNotFound, "IpGroup not found", err)
		return
	}
	return
}

func (a *IpGroupAdmin) GetIpGroup(ctx context.Context, reference *BaseReference) (ipGroup *model.IpGroup, err error) {
	logger.Infof("ENTER IpGroupAdmin.GetIpGroup: reference=%+v", reference)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT IpGroupAdmin.GetIpGroup: error=%v", err)
		} else {
			logger.Info("EXIT IpGroupAdmin.GetIpGroup: success")
		}
	}()
	if reference == nil || (reference.ID == "" && reference.Name == "") {
		err = NewCLError(ErrInvalidParameter, "IpGroup base reference must be provided with either uuid or name", nil)
		return
	}
	if reference.ID != "" {
		ipGroup, err = a.GetIpGroupByUUID(ctx, reference.ID)
		return
	}
	return
}

func (a *IpGroupAdmin) Update(ctx context.Context, ipGroup *model.IpGroup, name string, typeName string, ipGroupType int) (ipGroupTemp *model.IpGroup, err error) {
	logger.Infof("ENTER IpGroupAdmin.Update: id=%d, name=%s, typeName=%s, ipGroupType=%d", ipGroup.ID, name, typeName, ipGroupType)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT IpGroupAdmin.Update: error=%v", err)
		} else {
			logger.Info("EXIT IpGroupAdmin.Update: success")
		}
	}()
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckResourceOrg(model.OrgWriter, ipGroup.Owner)
	if !permit {
		logger.Error("Not authorized to update the ip group")
		err = NewCLError(ErrPermissionDenied, "Not authorized to update the ip group", nil)
		return
	}
	if name != "" && ipGroup.Name != name {
		ipGroup.Name = name
	}
	err = db.Model(&model.IpGroup{}).Where("id = ?", ipGroup.ID).Updates(map[string]interface{}{
		"name":    ipGroup.Name,
		"type":    typeName,
		"type_id": ipGroupType,
	}).Error
	if err != nil {
		logger.Errorf("Failed to save ipGroups, err=%v", err)
		err = NewCLError(ErrIpGroupUpdateFailed, "Failed to update ip group", err)
		return ipGroup, err
	}
	return ipGroup, nil
}

func (a *IpGroupAdmin) List(ctx context.Context, offset, limit int64, order, query string) (total int64, ipGroups []*model.IpGroup, err error) {
	logger.Infof("ENTER IpGroupAdmin.List: offset=%d, limit=%d, order=%s, query=%s", offset, limit, order, query)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT IpGroupAdmin.List: error=%v", err)
		} else {
			logger.Infof("EXIT IpGroupAdmin.List: total=%d, count=%d", total, len(ipGroups))
		}
	}()
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	query, args := memberShip.GetOrgFilter()
	if limit == 0 {
		limit = 16
	}
	if order == "" {
		order = "created_at"
	}
	ipGroups = []*model.IpGroup{}
	if err = db.Model(&model.IpGroup{}).Where(query, args...).Where(query).Count(&total).Error; err != nil {
		logger.Errorf("IpGroupAdmin.List: count error, err=%v", err)
		err = NewCLError(ErrSQLSyntaxError, "Failed to count ip groups", err)
		return
	}
	db = dbs.Sortby(db.Offset(offset).Limit(limit), order)
	if err = db.Preload("Subnets").Preload("DictionaryType").Preload("FloatingIPs", func(db *gorm.DB) *gorm.DB {
		return db.Order("floating_ips.updated_at")
	}).Preload("FloatingIPs.Subnet").Preload("FloatingIPs.Subnet.Group").Preload("FloatingIPs.Subnet.Group.DictionaryType").Preload("FloatingIPs.Group").Preload("FloatingIPs.Group.DictionaryType").Where(query, args...).Where(query).Find(&ipGroups).Error; err != nil {
		logger.Errorf("IpGroupAdmin.List: find error, err=%v", err)
		err = NewCLError(ErrSQLSyntaxError, "Failed to find ip groups", err)
		return
	}
	for _, ipGroup := range ipGroups {
		var names []string
		for _, subnet := range ipGroup.Subnets {
			names = append(names, subnet.Name)
		}
		ipGroup.SubnetNames = strings.Join(names, ",")
		var floatingIpNames []string
		for _, floatingIp := range ipGroup.FloatingIPs {
			floatingIpNames = append(floatingIpNames, floatingIp.Name)
		}
		ipGroup.FloatingIPNames = strings.Join(floatingIpNames, ",")
	}
	logger.Infof("IpGroupAdmin.List: success, total=%d, count=%d", total, len(ipGroups))
	return
}

func (v *IpGroupView) List(c *macaron.Context, store session.Store) {
	logger.Infof("ENTER IpGroupView.List: query=%s", c.Req.URL.RawQuery)
	defer logger.Info("EXIT IpGroupView.List")
	memberShip := GetMemberShip(c.Req.Context())
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
	if query != "" {
		query = fmt.Sprintf("name like '%%%s%%'", query)
	}
	total, ipGroups, err := ipGroupAdmin.List(c.Req.Context(), offset, limit, order, query)
	if err != nil {
		logger.Error("Failed to list ipGroup(s)", err)
		c.Data["ErrorMsg"] = err.Error()
		c.Error(500)
		return
	}
	pages := GetPages(total, limit)
	c.Data["IpGroups"] = ipGroups
	c.Data["Total"] = total
	c.Data["Pages"] = pages
	c.Data["Query"] = query
	c.HTML(200, "ipgroups")
}

func (v *IpGroupView) Edit(c *macaron.Context, store session.Store) {
	logger.Infof("ENTER IpGroupView.Edit: id=%s", c.Params("id"))
	defer logger.Info("EXIT IpGroupView.Edit")
	id := c.Params("id")
	if id == "" {
		c.Data["ErrorMsg"] = "Id is Empty"
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	ipGroupID, err := strconv.Atoi(id)
	if err != nil {
		logger.Error("Failed to get input id, %v", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	db := DB()
	ipGroup := &model.IpGroup{Model: model.Model{ID: int64(ipGroupID)}}
	err = db.Preload("Subnets").Preload("DictionaryType").Take(ipGroup).Error
	if err != nil {
		logger.Error("Failed to query ipgroup", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	var ipGroupTypes []model.Dictionary
	if err := db.Where("category = ?", "ipgroup").Find(&ipGroupTypes).Error; err != nil {
		c.Data["ErrorMsg"] = "Failed to load ipGroup categorys"
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	c.Data["IpGroup"] = ipGroup
	c.Data["IpGroupTypes"] = ipGroupTypes
	c.HTML(200, "ipgroups_patch")
}

func (v *IpGroupView) Change(c *macaron.Context, store session.Store) {
	logger.Infof("ENTER IpGroupView.Change: id=%s, query=%s", c.Params("id"), c.Req.URL.RawQuery)
	defer logger.Info("EXIT IpGroupView.Change")
	ctx := c.Req.Context()
	id := c.Params("id")
	if id == "" {
		c.Data["ErrorMsg"] = "Id is Empty"
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	ipGroupID, err := strconv.Atoi(id)
	if err != nil {
		logger.Error("Failed to get input id, %v", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	redirectTo := "../ipgroups"
	name := c.QueryTrim("name")
	typeName := c.QueryTrim("type")
	ipGroupType := c.QueryTrim("category")
	ipGroupTypeInt, err := strconv.Atoi(ipGroupType)
	if err != nil {
		c.HTML(500, err.Error())
		return
	}
	ipGroup, err := ipGroupAdmin.Get(ctx, int64(ipGroupID))
	if err != nil {
		c.HTML(500, err.Error())
		return
	}
	ipGroup, err = ipGroupAdmin.Update(ctx, ipGroup, name, typeName, ipGroupTypeInt)
	if err != nil {
		c.HTML(500, err.Error())
		return
	}
	c.Redirect(redirectTo)
}

func (v *IpGroupView) Patch(c *macaron.Context, store session.Store) {
	logger.Infof("ENTER IpGroupView.Patch: id=%s, query=%s", c.Params("id"), c.Req.URL.RawQuery)
	defer logger.Info("EXIT IpGroupView.Patch")
	ctx := c.Req.Context()
	id := c.Params("id")
	if id == "" {
		c.Data["ErrorMsg"] = "Id is Empty"
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	ipGroupID, err := strconv.Atoi(id)
	if err != nil {
		logger.Error("Failed to get input id, %v", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	redirectTo := "../ipgroups"
	name := c.QueryTrim("name")
	typeName := c.QueryTrim("type")
	ipGroupType := c.QueryTrim("category")
	var ipGroupTypeInt int
	if ipGroupType == "" {
		ipGroupTypeInt = 0
	} else {
		ipGroupTypeInt, err = strconv.Atoi(ipGroupType)
		if err != nil {
			c.HTML(500, err.Error())
			return
		}
	}
	logger.Infof("ipGroupTypeInt: %d", ipGroupTypeInt)
	if err != nil {
		c.HTML(500, err.Error())
		return
	}
	ipGroup, err := ipGroupAdmin.Get(ctx, int64(ipGroupID))
	if err != nil {
		c.HTML(500, err.Error())
		return
	}
	ipGroup, err = ipGroupAdmin.Update(ctx, ipGroup, name, typeName, ipGroupTypeInt)
	if err != nil {
		c.HTML(500, err.Error())
		return
	}
	c.Redirect(redirectTo)
}

func (v *IpGroupView) Delete(c *macaron.Context, store session.Store) (err error) {
	logger.Infof("ENTER IpGroupView.Delete: id=%s", c.Params("id"))
	defer func() {
		if err != nil {
			logger.Errorf("EXIT IpGroupView.Delete: error=%v", err)
		} else {
			logger.Info("EXIT IpGroupView.Delete: success")
		}
	}()
	ctx := c.Req.Context()
	id := c.Params("id")
	if id == "" {
		c.Data["ErrorMsg"] = "id does not exist"
		c.Error(http.StatusBadRequest)
		return
	}
	ipGroupID, err := strconv.Atoi(id)
	if err != nil {
		logger.Error("Failed to get ipGroup id ", err)
		c.Data["ErrorMsg"] = err.Error()
		c.Error(http.StatusBadRequest)
		return
	}
	ipGroup, err := ipGroupAdmin.Get(ctx, int64(ipGroupID))
	if err != nil {
		logger.Error("Failed to get ipGroup ", err)
		c.Data["ErrorMsg"] = err.Error()
		c.Error(http.StatusBadRequest)
		return
	}
	err = ipGroupAdmin.Delete(ctx, ipGroup)
	if err != nil {
		logger.Error("Failed to delete ipGroup ", err)
		c.Data["ErrorMsg"] = err.Error()
		c.Error(http.StatusBadRequest)
		return
	}
	c.JSON(200, map[string]interface{}{
		"redirect": "/ipgroups",
	})
	return
}

func (v *IpGroupView) New(c *macaron.Context, store session.Store) {
	logger.Infof("ENTER IpGroupView.New")
	defer logger.Info("EXIT IpGroupView.New")
	memberShip := GetMemberShip(c.Req.Context())
	permit := memberShip.CheckSystemPermission()
	if !permit {
		logger.Error("Not authorized for this operation")
		c.Data["ErrorMsg"] = "Not authorized for this operation"
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	var ipGroupTypes []model.Dictionary
	db := dbs.DB()
	if err := db.Where("category = ?", "ipgroup").Find(&ipGroupTypes).Error; err != nil {
		c.Data["ErrorMsg"] = "Failed to load ipgroup categorys"
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	c.Data["IpGroupTypes"] = ipGroupTypes
	c.HTML(200, "ipgroups_new")
}

func (v *IpGroupView) Create(c *macaron.Context, store session.Store) {
	logger.Infof("ENTER IpGroupView.Create: query=%s", c.Req.URL.RawQuery)
	defer logger.Info("EXIT IpGroupView.Create")
	ctx := c.Req.Context()
	redirectTo := "/ipgroups"
	name := c.QueryTrim("name")
	typeName := c.QueryTrim("type")
	ipGroupType := c.QueryTrim("category")
	logger.Infof("ipGroupType: %s", ipGroupType)
	var ipGroupTypeInt int
	var err error
	if ipGroupType == "" {
		ipGroupTypeInt = 0
	} else {
		ipGroupTypeInt, err = strconv.Atoi(ipGroupType)
		if err != nil {
			c.HTML(500, err.Error())
			return
		}
	}
	if err != nil {
		c.HTML(500, err.Error())
		return
	}

	if ipGroupTypeInt != 0 {
		var dictionaryEntry model.Dictionary
		db := dbs.DB()
		if err := db.Where("id = ? AND category = ?", int64(ipGroupTypeInt), "ipgroup").First(&dictionaryEntry).Error; err != nil {
			c.Data["ErrorMsg"] = "Invalid category ID"
			c.HTML(http.StatusBadRequest, "error")
			return
		}
	}

	_, err = ipGroupAdmin.Create(ctx, name, typeName, ipGroupTypeInt)
	if err != nil {
		logger.Error("Failed to create ipGroup, %v", err)
		c.HTML(500, "500")
		return
	}
	c.Redirect(redirectTo)
}
