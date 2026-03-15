package routes

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	. "web/src/common"
	"web/src/dbs"
	"web/src/model"

	"github.com/go-macaron/session"
	macaron "gopkg.in/macaron.v1"
)

var dictionaryAdmin = &DictionaryAdmin{}

type DictionaryAdmin struct{}

func (a *DictionaryAdmin) Create(ctx context.Context, category, name, value, shortname, subtype1, subtype2, subtype3 string) (dictionary *model.Dictionary, err error) {
	logger.Infof("ENTER DictionaryAdmin.Create: category=%s, name=%s, value=%s", category, name, value)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT DictionaryAdmin.Create: error=%v", err)
		} else {
			logger.Infof("EXIT DictionaryAdmin.Create: success, dictionaryID=%d", dictionary.ID)
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckSystemPermission()
	if !permit {
		err = NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
		return
	}
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	if value == "" {
		logger.Errorf("Value cannot be empty")
		err = NewCLError(ErrInvalidParameter, "Value cannot be empty", nil)
		return
	}
	dictionary = &model.Dictionary{
		Category:  category,
		Name:      name,
		Value:     value,
		ShortName: shortname,
		SubType1:  subtype1,
		SubType2:  subtype2,
		SubType3:  subtype3,
	}
	err = db.Create(dictionary).Error
	return
}

func (a *DictionaryAdmin) Get(ctx context.Context, id int64) (dictionary *model.Dictionary, err error) {
	logger.Infof("ENTER DictionaryAdmin.Get: id=%d", id)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT DictionaryAdmin.Get: error=%v", err)
		} else {
			logger.Infof("EXIT DictionaryAdmin.Get: success, dictionaryUUID=%s", dictionary.UUID)
		}
	}()
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	orgQuery, orgArgs := memberShip.GetOrgFilter()
	dictionary = &model.Dictionary{Model: model.Model{ID: id}}
	if err = db.Where(orgQuery, orgArgs...).First(dictionary, id).Error; err != nil {
		return nil, NewCLError(ErrDictionaryRecordsNotFound, "Dictionary not found", err)
	}
	return
}

func (a *DictionaryAdmin) List(ctx context.Context, offset, limit int64, order string, query string) (total int64, dictionaries []*model.Dictionary, err error) {
	logger.Infof("ENTER DictionaryAdmin.List: offset=%d, limit=%d, order=%s, query=%s", offset, limit, order, query)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT DictionaryAdmin.List: error=%v", err)
		} else {
			logger.Infof("EXIT DictionaryAdmin.List: total=%d, count=%d", total, len(dictionaries))
		}
	}()
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	orgQuery, orgArgs := memberShip.GetOrgFilter()
	if limit == 0 {
		limit = 16
	}

	if order == "" {
		order = "created_at"
	}

	dictionaries = []*model.Dictionary{}
	q := query
	if q != "" {
		q = fmt.Sprintf("name like '%%%s%%'", q)
	}
	if err = db.Model(&model.Dictionary{}).Where(orgQuery, orgArgs...).Where(q).Count(&total).Error; err != nil {
		logger.Errorf("DictionaryAdmin.List: count error, err=%v", err)
		return 0, nil, NewCLError(ErrSQLSyntaxError, "Failed to count dictionaries", err)
	}
	db = dbs.Sortby(db.Offset(offset).Limit(limit), order)
	if err = db.Where(orgQuery, orgArgs...).Where(q).Find(&dictionaries).Error; err != nil {
		logger.Errorf("DictionaryAdmin.List: find error, err=%v", err)
		return 0, nil, NewCLError(ErrSQLSyntaxError, "Failed to find dictionaries", err)
	}
	logger.Debugf("DictionaryAdmin.List: success, total=%d, count=%d", total, len(dictionaries))
	return
}

func (a *DictionaryAdmin) GetDictionaryByUUID(ctx context.Context, uuID string) (dictionary *model.Dictionary, err error) {
	logger.Infof("ENTER DictionaryAdmin.GetDictionaryByUUID: uuID=%s", uuID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT DictionaryAdmin.GetDictionaryByUUID: error=%v", err)
		} else {
			logger.Infof("EXIT DictionaryAdmin.GetDictionaryByUUID: success, id=%d", dictionary.ID)
		}
	}()
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	orgQuery, orgArgs := memberShip.GetOrgFilter()
	dictionary = &model.Dictionary{}
	err = db.Where(orgQuery, orgArgs...).Where("uuid = ?", uuID).Take(dictionary).Error
	if err != nil {
		return nil, NewCLError(ErrDictionaryRecordsNotFound, "Dictionary records not found", err)
	}
	return
}

func (a *DictionaryAdmin) Update(ctx context.Context, dictionaries *model.Dictionary, category, name, value, shortname, subtype1, subtype2, subtype3 string) (dictionary *model.Dictionary, err error) {
	logger.Infof("ENTER DictionaryAdmin.Update: id=%d", dictionaries.ID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT DictionaryAdmin.Update: error=%v", err)
		} else {
			logger.Info("EXIT DictionaryAdmin.Update: success")
		}
	}()
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckSystemPermission()
	if !permit {
		logger.Error("Not authorized to update the dictionary")
		err = NewCLError(ErrPermissionDenied, "Not authorized to update the dictionary", nil)
		return
	}
	if category != "" && dictionaries.Category != category {
		dictionaries.Category = category
	}
	if name != "" && dictionaries.Name != name {
		dictionaries.Name = name
	}
	if value != "" && dictionaries.Value != value {
		dictionaries.Value = value
	}
	if shortname != "" && dictionaries.ShortName != shortname {
		dictionaries.ShortName = shortname
	}
	if subtype1 != "" && dictionaries.SubType1 != subtype1 {
		dictionaries.SubType1 = subtype1
	}
	if subtype2 != "" && dictionaries.SubType2 != subtype2 {
		dictionaries.SubType2 = subtype2
	}
	if subtype3 != "" && dictionaries.SubType3 != subtype3 {
		dictionaries.SubType3 = subtype3
	}
	err = db.Model(dictionaries).Updates(dictionaries).Error
	if err != nil {
		logger.Errorf("DictionaryAdmin.Update: save error, err=%v", err)
		return nil, NewCLError(ErrDictionaryUpdateFailed, "Failed to update dictionary", err)
	}
	dictionary = dictionaries
	logger.Debugf("DictionaryAdmin.Update: success, uuid=%s, dictionary=%+v", dictionary.UUID, dictionary)
	return
}

func (a *DictionaryAdmin) Find(ctx context.Context, category, value string) (dictionary *model.Dictionary, err error) {
	logger.Infof("ENTER DictionaryAdmin.Find: category=%s, value=%s", category, value)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT DictionaryAdmin.Find: error=%v", err)
		} else {
			logger.Infof("EXIT DictionaryAdmin.Find: success, id=%d", dictionary.ID)
		}
	}()
	db := DB()
	dictionary = &model.Dictionary{}
	err = db.Where("category = ? AND value = ?", category, value).Take(dictionary).Error
	if err != nil {
		return nil, NewCLError(ErrDictionaryRecordsNotFound, "Dictionary records not found", err)
	}
	return
}

func (a *DictionaryAdmin) Delete(ctx context.Context, dictionaries *model.Dictionary) (err error) {
	logger.Infof("ENTER DictionaryAdmin.Delete: id=%d", dictionaries.ID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT DictionaryAdmin.Delete: error=%v", err)
		} else {
			logger.Info("EXIT DictionaryAdmin.Delete: success")
		}
	}()
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckSystemPermission()
	if !permit {
		logger.Error("Not authorized to delete the dictionary")
		err = NewCLError(ErrPermissionDenied, "Not authorized to delete the dictionary", nil)
		return
	}
	dictionaries.Value = fmt.Sprintf("%s-%d", dictionaries.Value, dictionaries.CreatedAt.Unix())
	err = db.Model(dictionaries).Update("value", dictionaries.Value).Error
	if err != nil {
		logger.Error("DB failed to update dictionary value", err)
		return NewCLError(ErrDictionaryUpdateFailed, "Failed to update dictionary value", err)
	}
	if err = db.Delete(dictionaries).Error; err != nil {
		logger.Errorf("DictionaryAdmin.Delete: db delete error, err=%v", err)
		return NewCLError(ErrDictionaryDeleteFailed, "Failed to delete dictionary", err)
	}
	return
}

type DictionaryView struct{}

func (v *DictionaryView) List(c *macaron.Context, store session.Store) {
	logger.Infof("ENTER DictionaryView.List: query=%s", c.Req.URL.RawQuery)
	defer logger.Info("EXIT DictionaryView.List")
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
	total, dictionaries, err := dictionaryAdmin.List(c.Req.Context(), offset, limit, order, query)
	if err != nil {
		logger.Error("Failed to list dictionaries", err)
		c.Data["ErrorMsg"] = err.Error()
		c.Error(500)
		return
	}
	pages := GetPages(total, limit)
	c.Data["Dictionaries"] = dictionaries
	c.Data["Total"] = total
	c.Data["Pages"] = pages
	c.Data["Query"] = query
	c.HTML(200, "dictionaries")
}

func (v *DictionaryView) New(c *macaron.Context, store session.Store) {
	logger.Info("ENTER DictionaryView.New")
	defer logger.Info("EXIT DictionaryView.New")
	c.HTML(200, "dictionaries_new")
}

func (v *DictionaryView) Create(c *macaron.Context, store session.Store) {
	logger.Infof("ENTER DictionaryView.Create: query=%s", c.Req.URL.RawQuery)
	defer logger.Info("EXIT DictionaryView.Create")
	ctx := c.Req.Context()
	redirectTo := "/dictionaries"
	category := c.QueryTrim("category")
	name := c.QueryTrim("name")
	value := c.QueryTrim("value")
	shortname := c.QueryTrim("shortname")
	subtype1 := c.QueryTrim("subtype1")
	subtype2 := c.QueryTrim("subtype2")
	subtype3 := c.QueryTrim("subtype3")

	var err error
	_, err = dictionaryAdmin.Create(ctx, category, name, value, shortname, subtype1, subtype2, subtype3)
	if err != nil {
		logger.Error("Failed to create dictionary, %v", err)
		c.HTML(500, "500")
		return
	}
	c.Redirect(redirectTo)
}

func (v *DictionaryView) Edit(c *macaron.Context, store session.Store) {
	logger.Infof("ENTER DictionaryView.Edit: params=%v", c.Params)
	defer logger.Info("EXIT DictionaryView.Edit")
	id := c.Params("id")
	if id == "" {
		c.Data["ErrorMsg"] = "Id is Empty"
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	dictionaryID, err := strconv.Atoi(id)
	if err != nil {
		logger.Error("Failed to get input id, %v", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	db := DB()
	dictionary := &model.Dictionary{Model: model.Model{ID: int64(dictionaryID)}}
	err = db.Set("gorm:auto_preload", true).Take(dictionary).Error
	if err != nil {
		logger.Error("Failed to query dictionary", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	c.Data["Dictionary"] = dictionary
	c.HTML(200, "dictionaries_patch")
}

func (v *DictionaryView) Patch(c *macaron.Context, store session.Store) {
	logger.Infof("ENTER DictionaryView.Patch: params=%v, query=%s", c.Params, c.Req.URL.RawQuery)
	defer logger.Info("EXIT DictionaryView.Patch")
	ctx := c.Req.Context()
	id := c.Params("id")
	if id == "" {
		c.Data["ErrorMsg"] = "Id is Empty"
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	dictionaryID, err := strconv.Atoi(id)
	if err != nil {
		logger.Error("Failed to get input id, %v", err)
		c.Data["ErrorMsg"] = err.Error()
		c.HTML(http.StatusBadRequest, "error")
		return
	}
	redirectTo := "../dictionaries"
	category := c.QueryTrim("category")
	name := c.QueryTrim("name")
	value := c.QueryTrim("value")
	shortname := c.QueryTrim("shortname")
	subtype1 := c.QueryTrim("subtype1")
	subtype2 := c.QueryTrim("subtype2")
	subtype3 := c.QueryTrim("subtype3")
	dictionaries, err := dictionaryAdmin.Get(ctx, int64(dictionaryID))
	if err != nil {
		c.HTML(500, err.Error())
		return
	}
	_, err = dictionaryAdmin.Update(ctx, dictionaries, category, name, value, shortname, subtype1, subtype2, subtype3)
	if err != nil {
		c.HTML(500, err.Error())
		return
	}
	c.Redirect(redirectTo)
}

func (v *DictionaryView) Delete(c *macaron.Context, store session.Store) {
	logger.Infof("ENTER DictionaryView.Delete: params=%v", c.Params)
	defer logger.Info("EXIT DictionaryView.Delete")
	ctx := c.Req.Context()
	id := c.Params("id")
	if id == "" {
		c.Data["ErrorMsg"] = "id does not exist"
		c.Error(http.StatusBadRequest)
		return
	}
	dictionaryID, err := strconv.Atoi(id)
	if err != nil {
		logger.Error("Failed to get dictionary id ", err)
		c.Data["ErrorMsg"] = err.Error()
		c.Error(http.StatusBadRequest)
		return
	}
	dictionaries, err := dictionaryAdmin.Get(ctx, int64(dictionaryID))
	if err != nil {
		logger.Error("Failed to get dictionary ", err)
		c.Data["ErrorMsg"] = err.Error()
		c.Error(http.StatusBadRequest)
		return
	}
	err = dictionaryAdmin.Delete(ctx, dictionaries)
	if err != nil {
		logger.Error("Failed to delete dictionary ", err)
		c.Data["ErrorMsg"] = err.Error()
		c.Error(http.StatusBadRequest)
		return
	}
	c.JSON(200, map[string]interface{}{
		"redirect": "/dictionaries",
	})
}
