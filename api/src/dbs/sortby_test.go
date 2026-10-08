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
	"reflect"
	"testing"

	"gorm.io/gorm"
)

func TestNewOrders(t *testing.T) {
	empty := [][2]string{}
	mapping := [][2]string{
		[2]string{"flavor.name", "flavor"},
		[2]string{"image.id", "image"},
	}
	fixtues := []struct {
		s      string
		orders []string
		m      [][2]string
	}{
		{"+name", []string{"name"}, nil},
		{"-name", []string{"name DESC"}, empty},
		{"-name,created_at", []string{"name DESC", "created_at"}, empty},
		{"-name,created_at", []string{"name DESC", "created_at"}, mapping},
		{"-name,created_at,image.id", []string{"name DESC", "created_at", "image"}, mapping},
		{"-name,created_at,image.id,flavor.name", []string{"name DESC", "created_at", "image", "flavor"}, mapping},
	}
	for _, f := range fixtues {
		var orders []string
		if f.m != nil {
			orders = NewOrders(f.s, f.m...)
		} else {
			orders = NewOrders(f.s)
		}
		if !reflect.DeepEqual(f.orders, orders) {
			t.Fatal(f.orders, orders)
		}
	}
}

func TestNewOrdersRejectsNonIdentifiers(t *testing.T) {
	fixtures := []struct {
		s      string
		orders []string
	}{
		{"-created_at", []string{"created_at DESC"}},
		{"instances.hostname", []string{"instances.hostname"}},
		{"name;drop table users", nil},
		{"(select 1)", nil},
		{"name desc", nil},
		{"-id,case when 1=1 then name end", []string{"id DESC"}},
		{"name.x.y", nil},
	}
	for _, f := range fixtures {
		if orders := NewOrders(f.s); !reflect.DeepEqual(f.orders, orders) {
			t.Fatalf("NewOrders(%q) = %v, want %v", f.s, orders, f.orders)
		}
	}
}

func TestContains(t *testing.T) {
	type ContainsItem struct {
		gorm.Model
		Name  string
		Owner int
	}
	db := DB()
	db.Migrator().DropTable(&ContainsItem{})
	if err := db.AutoMigrate(&ContainsItem{}); err != nil {
		t.Fatal(err)
	}
	for _, item := range []ContainsItem{{Name: "web-01", Owner: 1}, {Name: "db_01", Owner: 1}, {Name: "100%", Owner: 1}, {Name: `C:\temp`, Owner: 1}, {Name: "foo%bar", Owner: 1}, {Name: "other", Owner: 2}} {
		if err := db.Create(&item).Error; err != nil {
			t.Fatal(err)
		}
	}
	names := func(value string, columns ...string) []string {
		var items []ContainsItem
		if err := db.Where("owner = ?", 1).Scopes(Contains(value, columns...)).Order("id").Find(&items).Error; err != nil {
			t.Fatal(value, err)
		}
		result := []string{}
		for _, item := range items {
			result = append(result, item.Name)
		}
		return result
	}
	fixtures := []struct {
		value string
		names []string
	}{
		{"", []string{"web-01", "db_01", "100%", `C:\temp`, "foo%bar"}},
		{"01", []string{"web-01", "db_01"}},
		// Wildcards and the escape character match literally
		{"_", []string{"db_01"}},
		{"%", []string{"100%", "foo%bar"}},
		{`\`, []string{`C:\temp`}},
		{`C:\temp`, []string{`C:\temp`}},
		// An unescaped trailing backslash would turn into an escape for the closing wildcard
		{`foo\`, []string{}},
		// Injection attempts only match literally and never escape the owner filter
		{"%') OR 1=1 OR ('", []string{}},
		{"' OR '1'='1", []string{}},
	}
	for _, f := range fixtures {
		if got := names(f.value, "name"); !reflect.DeepEqual(f.names, got) {
			t.Fatalf("Contains(%q) = %v, want %v", f.value, got, f.names)
		}
	}
	// Multiple columns are OR-ed inside one group, still AND-ed with the other conditions
	if got := names("other", "name", "name"); !reflect.DeepEqual([]string{}, got) {
		t.Fatalf("multi-column Contains leaked rows: %v", got)
	}
}
