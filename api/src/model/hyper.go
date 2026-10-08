/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

History:
   Date     Who ID    Description
   -------- --- ---   -----------
   01/13/19 nanjj  Initial code

*/

package model

import (
	context "context"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"api/src/dbs"
)

type Hyper struct {
	ID           int64 `gorm:"primary_key"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
	UUID         string `gorm:"type:varchar(64);index"`
	Hostid       int32  `gorm:"uniqueIndex"`
	Hostname     string `gorm:"type:varchar(64)"`
	Status       int32
	Parentid     int32
	Children     int32
	Duration     int64
	HostIP       string
	RouteIP      string
	VirtType     string
	CpuModel     string
	CpuOverRate  float32 `gorm:"default:1.0"`
	MemOverRate  float32 `gorm:"default:1.0"`
	DiskOverRate float32 `gorm:"default:1.0"`
	ZoneID       int64
	Zone         *Zone     `gorm:"foreignkey:ZoneID"`
	Resource     *Resource `gorm:"foreignKey:Hostid;references:Hostid"`
	Remark       string    `gorm:"type:varchar(512);default:''"`
	// When cland took the host offline (status 10), nil while it is online; how long it has been away decides
	// whether its instances may be recovered elsewhere (shared-storage-design.md §11.1)
	OfflineAt *time.Time
	// Status the host had when it went offline: disabled (0) and maintaining (2) are set by an admin and come back
	// with the host, which reports itself active
	OfflinePrior *int32
	// When the host last applied a reconcile of its instances (§11.4), and for which boot
	ReconciledAt   *time.Time
	ReconciledBoot string `gorm:"type:varchar(64)"`
}

func (hyper *Hyper) BeforeCreate(tx *gorm.DB) (err error) {
	if hyper.UUID == "" {
		hyper.UUID = uuid.New().String()
		logger.Debugf("Create a new hypervisor with uuid: %s", hyper.UUID)
	}
	return
}

func (hyper *Hyper) GetStatus() string {
	if status, ok := HyperStatusValues[hyper.Status]; ok {
		return status
	}
	return HYPER_INIT
}

func init() {
	dbs.AutoMigrate(&Hyper{})
}

const (
	HYPER_INIT          = ""
	HYPER_DISABLED      = "disabled"
	HYPER_ACTIVE        = "active"
	HYPER_MAINTAINING   = "maintaining"
	HYPER_DEPLOYING     = "deploying"
	HYPER_DEPLOY_FAILED = "deploy_failed"
)

// HyperStatusOffline is the status cland's topology report gives a host it lost: not one of the statuses below,
// which an admin or the host itself sets
const HyperStatusOffline int32 = 10

var (
	HyperStatusValues = map[int32]string{
		0: HYPER_DISABLED,
		1: HYPER_ACTIVE,
		2: HYPER_MAINTAINING,
		4: HYPER_DEPLOYING,
		5: HYPER_DEPLOY_FAILED,
	}
	HyperStatusNames = map[string]int32{
		HYPER_INIT:          0,
		HYPER_DISABLED:      0,
		HYPER_ACTIVE:        1,
		HYPER_MAINTAINING:   2,
		HYPER_DEPLOYING:     4,
		HYPER_DEPLOY_FAILED: 5,
	}
)

/*
func (hyper *Hyper) LoadRequest(h *hypers.Hyper) {
	hyper.Hostid = h.GetId()
	hyper.Hostname = h.GetHostname()
	hyper.Parentid = h.GetParentid()
	hyper.Status = HyperStatusNames[h.GetStatus()]
	hyper.Duration = h.GetDuration()
}

func (hyper *Hyper) ToReply() (h *hypers.Hyper) {
	h = &hypers.Hyper{
		Id:       hyper.Hostid,
		Hostname: hyper.Hostname,
		Status:   HyperStatusValues[hyper.Status],
		Parentid: hyper.Parentid,
		Duration: hyper.Duration,
	}
	return
}
*/

func (hyper *Hyper) LoadControl(control string) {
	items := strings.Split(control, " ")
	for _, item := range items {
		kv := strings.Split(item, "=")
		if len(kv) != 2 {
			continue
		}
		k, v := kv[0], kv[1]
		if v == "" {
			continue
		}
		switch k {
		case "id":
			if id, err := strconv.Atoi(v); err == nil {
				hyper.Hostid = int32(id)
			}
		case "hostname":
			hyper.Hostname = v
		case "num":
			if num, err := strconv.Atoi(v); err == nil {
				hyper.Children = int32(num)
			}
		}
	}
}

func (hyper *Hyper) LoadCommand(command string) {
	// 6845,cloudland-136,1
	items := strings.Split(command, ",")
	if len(items) != 3 {
		return
	}
	if items[1] != "" {
		hyper.Hostname = items[1]
	}
	if items[0] != "" {
		if id, err := strconv.Atoi(items[0]); err == nil {
			hyper.Hostid = int32(id)
		}
		if status, err := strconv.Atoi(items[2]); err == nil {
			if (status == 10) || (hyper.Status > 0 && hyper.Status != int32(status)) {
				hyper.Status = int32(status)
			}
		}
	}
}

func (hyper *Hyper) Updates(ctx context.Context, values *Hyper) (err error) {
	db := dbs.DBContext(ctx)
	where := map[string]interface{}{
		"hostid": values.Hostid,
	}
	if err = db.FirstOrCreate(hyper, where).Error; err != nil {
		logger.Ctx(ctx).Error(err)
		return
	}

	if err = db.Model(hyper).Updates(values).Error; err != nil {
		logger.Ctx(ctx).Error(err)
		return
	}
	return
}
