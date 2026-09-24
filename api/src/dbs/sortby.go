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
	"fmt"
	"regexp"
	"strings"

	"gorm.io/gorm"
)

// orderColumnPattern limits sort columns to plain (optionally table-qualified) identifiers:
// the sort string can come from request parameters and is placed into ORDER BY unquoted
var orderColumnPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)?$`)

// Sortby Sort by `s` defined at api handbook collections sorting
func Sortby(db *gorm.DB, s string, m ...[2]string) *gorm.DB {
	orders := NewOrders(s, m...)
	for _, order := range orders {
		db = db.Order(order)
	}
	return db
}

// NewOrders new orders
// s: sort string defined in api handbook collections sorting
// m: mappings
func NewOrders(s string, m ...[2]string) (orders []string) {
	if s == "" {
		return
	}
	mapping := func(k string) (v string) {
		for _, p := range m {
			if k == p[0] {
				v = p[1]
				return
			}
		}
		return k
	}
	items := strings.Split(s, ",")
	for _, item := range items {
		item = strings.TrimSpace(item)
		item = mapping(item)
		if item == "" {
			continue
		}
		desc := false
		switch item[0] {
		case '-':
			desc = true
			item = item[1:]
		case '+':
			item = item[1:]
		}
		if !orderColumnPattern.MatchString(item) {
			continue
		}
		if desc {
			item = fmt.Sprintf("%s DESC", item)
		}
		orders = append(orders, item)
	}
	return
}

// likeEscaper escapes LIKE wildcards so user input matches literally (backslash is the default escape character in PostgreSQL and SQLite needs ESCAPE)
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// Contains returns a scope matching rows where any of the columns contains value as a substring.
// The value is bound as a parameter, so it is safe for request input; columns must be trusted identifiers.
// An empty value adds no condition.
func Contains(value string, columns ...string) func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if value == "" || len(columns) == 0 {
			return db
		}
		pattern := "%" + likeEscaper.Replace(value) + "%"
		conds := make([]string, len(columns))
		args := make([]interface{}, len(columns))
		for i, column := range columns {
			conds[i] = column + ` LIKE ? ESCAPE '\'`
			args[i] = pattern
		}
		return db.Where(strings.Join(conds, " OR "), args...)
	}
}

// OrderByID orders preloaded associations by primary key: without an ORDER BY, PostgreSQL returns rows in
// storage order, which changes after updates (e.g. load balancer backends reorder after an edit)
func OrderByID(db *gorm.DB) *gorm.DB {
	return db.Order("id")
}
