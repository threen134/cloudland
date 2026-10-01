/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package model

import (
	"time"

	"api/src/dbs"

	"gorm.io/gorm"
)

const (
	TgwStatusAvailable = "available"

	TgwAttachmentAttaching = "attaching"
	TgwAttachmentAvailable = "available"
	TgwAttachmentDetaching = "detaching"
	TgwAttachmentError     = "error"

	TgwRouteStatic    = "static"
	TgwRouteBlackhole = "blackhole"

	TgwNodeOK    = "ok"
	TgwNodeError = "error"
	// The node left the gateway and has not confirmed the empty state yet
	TgwNodeLeaving = "leaving"
)

// TransitGateway connects the VPCs of one organization in a region (vpc-transit-gateway-plan.md). It is
// distributed: every node hosting a member VPC runs a tgw-<ID> netns and forwards between the members locally.
// Generation counts the changes of the desired state; every dispatch carries it and nodes drop older ones.
type TransitGateway struct {
	Model
	Owner       int64         `gorm:"index"` /* The organization ID of the resource */
	Name        string        `gorm:"type:varchar(64)"`
	Description string        `gorm:"type:varchar(256)"`
	Status      string        `gorm:"type:varchar(32)"`
	Generation  int64         `gorm:"not null;default:0"`
	OwnerInfo   *Organization `gorm:"-"` /* Transient: populated for SystemAdmin list view */
}

// TgwRouteTable is one routing domain of a gateway. Each attachment is associated with one table, which decides
// where the traffic entering the gateway from that VPC may go. The kernel table in tgw-<ID> is 1000 + Slot.
type TgwRouteTable struct {
	Model
	Owner int64
	TgwID int64  `gorm:"index"`
	Name  string `gorm:"type:varchar(64)"`
	// No default tag: false is the common value and GORM would replace an explicit false with the default
	IsDefault bool
	Slot      int32
}

// TgwAttachment connects one VPC to a gateway. Slot (0-127, unique in the gateway) picks the /31 of the veth
// pair tr-<ID> (VPC router) / ta-<ID> (gateway). Generation is the desired state generation that brought the
// attachment to its current status; the attachment becomes available once every node applied it.
type TgwAttachment struct {
	Model
	Owner        int64
	TgwID        int64   `gorm:"index"`
	RouterID     int64   `gorm:"index"`
	Router       *Router `gorm:"foreignkey:RouterID"`
	RouteTableID int64
	Slot         int32
	Status       string `gorm:"type:varchar(32)"`
	StatusReason string `gorm:"type:varchar(512)"`
	Generation   int64
}

// TgwPropagation makes the internal subnets of an attachment's VPC appear in a route table. Prefixes is a comma
// separated allow list: empty propagates every subnet, otherwise only the parts of the subnets inside it.
type TgwPropagation struct {
	Model
	Owner        int64
	TgwID        int64 `gorm:"index"`
	RouteTableID int64 `gorm:"index"`
	AttachmentID int64
	Prefixes     string `gorm:"type:varchar(1024)"`
}

// TgwRoute is a static route of a route table: a destination towards an attachment, or dropped (blackhole,
// AttachmentID 0). A static route wins over a propagated one with the same destination.
type TgwRoute struct {
	Model
	Owner        int64
	TgwID        int64  `gorm:"index"`
	RouteTableID int64  `gorm:"index"`
	Destination  string `gorm:"type:varchar(64)"`
	AttachmentID int64
	Type         string `gorm:"type:varchar(16)"`
}

// TgwNodeState is what a node reported applying for a gateway: the latest generation and whether it worked.
// Not soft-deleted; rows of nodes that left the gateway are removed.
type TgwNodeState struct {
	ID         int64 `gorm:"primaryKey"`
	TgwID      int64 `gorm:"uniqueIndex:uq_tgw_node_state"`
	Hyper      int32 `gorm:"uniqueIndex:uq_tgw_node_state"`
	Generation int64
	Status     string `gorm:"type:varchar(16)"`
	Reason     string `gorm:"type:varchar(512)"`
	UpdatedAt  time.Time
}

func init() {
	dbs.AutoMigrate(&TransitGateway{}, &TgwRouteTable{}, &TgwAttachment{}, &TgwPropagation{}, &TgwRoute{}, &TgwNodeState{})
	// The unique indexes cover the live rows only: a full one would make the soft-deleted row of a detached VPC,
	// a deleted route or table block attaching the VPC again or re-adding the same destination
	dbs.AutoUpgrade("transit_gateway_unique_indexes_v1", func(db *gorm.DB) error {
		for _, stmt := range []string{
			"CREATE UNIQUE INDEX IF NOT EXISTS uq_tgw_owner_name ON transit_gateways (owner, name) WHERE deleted_at IS NULL",
			"CREATE UNIQUE INDEX IF NOT EXISTS uq_tgw_route_table_name ON tgw_route_tables (tgw_id, name) WHERE deleted_at IS NULL",
			"CREATE UNIQUE INDEX IF NOT EXISTS uq_tgw_route_table_slot ON tgw_route_tables (tgw_id, slot) WHERE deleted_at IS NULL",
			"CREATE UNIQUE INDEX IF NOT EXISTS uq_tgw_attachment_router ON tgw_attachments (router_id) WHERE deleted_at IS NULL",
			"CREATE UNIQUE INDEX IF NOT EXISTS uq_tgw_attachment_slot ON tgw_attachments (tgw_id, slot) WHERE deleted_at IS NULL",
			"CREATE UNIQUE INDEX IF NOT EXISTS uq_tgw_propagation ON tgw_propagations (route_table_id, attachment_id) WHERE deleted_at IS NULL",
			"CREATE UNIQUE INDEX IF NOT EXISTS uq_tgw_route_destination ON tgw_routes (route_table_id, destination) WHERE deleted_at IS NULL",
		} {
			if err := db.Exec(stmt).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
