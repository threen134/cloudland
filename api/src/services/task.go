/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"context"
	"fmt"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"
)

var TaskAdmin = &TaskAdminService{}

type TaskAdminService struct{}

func (a *TaskAdminService) Get(ctx context.Context, taskID int64) (task *model.Task, err error) {
	logger.Infof("ENTER TaskAdmin.Get: taskID=%d", taskID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT TaskAdmin.Get: error=%v", err)
		} else {
			logger.Infof("EXIT TaskAdmin.Get: taskUUID=%s", task.UUID)
		}
	}()
	if taskID <= 0 {
		err = NewCLError(ErrInvalidParameter, fmt.Sprintf("Invalid task ID: %d", taskID), nil)
		logger.Error(err)
		return
	}
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	where, args := memberShip.GetOrgFilter()
	task = &model.Task{Model: model.Model{ID: taskID}}
	if err = db.Where(where, args...).Take(task).Error; err != nil {
		logger.Error("DB: query task failed", err)
		err = NewCLError(ErrTaskNotFound, "Task not found", err)
		return
	}
	permit := memberShip.CheckResourceOrg(model.OrgReader, task.Owner)
	if !permit {
		logger.Error("Not authorized to read the task")
		err = NewCLError(ErrPermissionDenied, "Not authorized to read the task", nil)
		return
	}
	return
}

func (a *TaskAdminService) GetTaskByUUID(ctx context.Context, uuid string) (task *model.Task, err error) {
	logger.Infof("ENTER TaskAdmin.GetTaskByUUID: uuid=%s", uuid)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT TaskAdmin.GetTaskByUUID: error=%v", err)
		} else {
			logger.Infof("EXIT TaskAdmin.GetTaskByUUID: taskID=%d", task.ID)
		}
	}()
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	where, args := memberShip.GetOrgFilter()
	task = &model.Task{}
	err = db.Where(where, args...).Where("uuid = ?", uuid).Take(task).Error
	if err != nil {
		logger.Error("DB: query task failed", err)
		err = NewCLError(ErrTaskNotFound, "Task not found", err)
		return
	}
	permit := memberShip.CheckResourceOrg(model.OrgReader, task.Owner)
	if !permit {
		logger.Error("Not authorized to read the task")
		err = NewCLError(ErrPermissionDenied, "Not authorized to read the task", nil)
		return
	}
	return
}

func (a *TaskAdminService) List(ctx context.Context, offset, limit int64, order string, query string, source string) (total int64, tasks []*model.Task, err error) {
	logger.Infof("ENTER TaskAdmin.List: offset=%d, limit=%d, order=%s, query=%s, source=%s", offset, limit, order, query, source)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT TaskAdmin.List: error=%v", err)
		} else {
			logger.Infof("EXIT TaskAdmin.List: total=%d, tasksCount=%d", total, len(tasks))
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

	if query != "" {
		query = fmt.Sprintf("name like '%%%s%%'", query)
	}
	queryBuilder, args := memberShip.GetOrgFilter()
	source_where := ""
	if source == string(model.TaskSourceManual) {
		source_where = fmt.Sprintf("source='%s'", source)
	} else if source == string(model.TaskSourceScheduler) {
		source_where = fmt.Sprintf("source='%s'", source)
	} else if source == string(model.TaskSourceMigration) {
		source_where = fmt.Sprintf("source='%s'", source)
	} else if source == "not_migration" || source == "" { // show all tasks except migration tasks
		source_where = fmt.Sprintf("source!='%s'", string(model.TaskSourceMigration))
	} else if source == "all" {
		source_where = ""
	} else {
		err = NewCLError(ErrInvalidParameter, fmt.Sprintf("Invalid task source %s", source), nil)
		return
	}

	tasks = []*model.Task{}
	if err = db.Model(&model.Task{}).Where(queryBuilder, args...).Where(query).Where(source_where).Count(&total).Error; err != nil {
		logger.Error("DB: count tasks failed", err)
		err = NewCLError(ErrSQLSyntaxError, "Failed to count tasks", err)
		return
	}
	db = dbs.Sortby(db.Offset(offset).Limit(limit), order)
	if err = db.Where(source_where).Where(queryBuilder, args...).Where(query).Find(&tasks).Error; err != nil {
		logger.Error("DB: query tasks failed", err)
		err = NewCLError(ErrSQLSyntaxError, "Failed to query tasks", err)
		return
	}
	permit := memberShip.IsSystemAdmin()
	if permit {
		db = db.Offset(0).Limit(-1)
		for _, task := range tasks {
			task.OwnerInfo = &model.Organization{Model: model.Model{ID: task.Owner}}
			if err = db.Take(task.OwnerInfo).Error; err != nil {
				logger.Error("DB: query owner info failed", err)
				err = NewCLError(ErrOwnerNotFound, "Owner organization not found", err)
				return
			}
		}
	}

	return
}
