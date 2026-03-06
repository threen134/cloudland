/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

History:
   Date     Who ID    Description
   -------- --- ---   -----------
   01/13/19 nanjj  Initial code

*/

package dbs

import (
	"testing"

	"github.com/jinzhu/gorm"
)

func TestQuery(t *testing.T) {
	type TestQuery01 struct {
		gorm.Model
		Name string
		Age  int32
	}
	db := DB()
	db.AutoMigrate(&TestQuery01{})
	db.Create(&TestQuery01{
		Name: "Test01",
	})
	db.Create(&TestQuery01{
		Name: "Test02",
	})
	rs := []TestQuery01{}
	err := db.Find(&rs).Error
	if err != nil || len(rs) != 3 {
		t.Fatal(rs, err)
	}
	db.Model(&TestQuery01{}).Update("age", 10)
	affected := db.RowsAffected
	if affected != 2 {
		t.Fatal(affected)
	}
	db.Where("age = 10").Delete(&TestQuery01{})
	affected = db.RowsAffected
	if affected != 2 {
		t.Fatal(affected)
	}
	rs = []TestQuery01{}
	err = db.Find(&rs).Error
	if err != nil || len(rs) != 1 {
		t.Fatal(rs, err)
	}
}
