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

	"gorm.io/gorm"
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
	if err != nil || len(rs) != 2 {
		t.Fatal(rs, err)
	}
	result := db.Model(&TestQuery01{}).Where("1 = 1").Update("age", 10)
	if result.RowsAffected != 2 {
		t.Fatal(result.RowsAffected)
	}
	result = db.Where("age = 10").Delete(&TestQuery01{})
	if result.RowsAffected != 2 {
		t.Fatal(result.RowsAffected)
	}
	rs = []TestQuery01{}
	err = db.Find(&rs).Error
	if err != nil || len(rs) != 0 {
		t.Fatal(rs, err)
	}
}
