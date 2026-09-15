/*
Copyright <holder> All Rights Reserved.
SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"
)

var SecruleAdmin = &SecruleAdminService{}

type SecruleAdminService struct{}

func (a *SecruleAdminService) ApplySecgroup(ctx context.Context, secgroup *model.SecurityGroup) (err error) {
	logger.Ctx(ctx).Infof("ENTER SecruleAdmin.ApplySecgroup: secgroupID=%d", secgroup.ID)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT SecruleAdmin.ApplySecgroup: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT SecruleAdmin.ApplySecgroup: success")
		}
	}()
	ctx, db := GetContextDB(ctx)
	err = secgroupAdmin.GetSecgroupInterfaces(ctx, secgroup)
	if err != nil {
		logger.Ctx(ctx).Error("DB failed to get security group related interfaces", err)
		return
	}
	for _, iface := range secgroup.Interfaces {
		logger.Ctx(ctx).Debugf("iface: %+v", iface)
		if iface.Instance > 0 {
			instance := &model.Instance{Model: model.Model{ID: iface.Instance}}
			err = db.Take(instance).Error
			if err != nil {
				logger.Ctx(ctx).Error("DB failed to get instance, %v", err)
				err = nil
				continue
			}
			if iface.Address != nil {
				err = ApplyInterface(ctx, instance, iface, false)
				if err != nil {
					logger.Ctx(ctx).Error("DB failed to apply interface, %v", err)
					err = nil
					continue
				}
			}
		}
	}
	return
}

// normalizeRulePorts 按协议校验并规范化端口字段：
//   - tcp/udp：1-65535（都未指定时表示全部端口，只指定一个时为单端口）
//   - icmp：port_min 为 ICMP type、port_max 为 ICMP code，-1 表示任意（apply_sg_rule.sh 据此生成 --icmp-type）
//   - gre/ipv6：不区分端口，统一存 -1
func normalizeRulePorts(protocol string, portMin, portMax int32) (int32, int32, error) {
	if protocol == "icmp" {
		return normalizeICMPTypeCode(portMin, portMax)
	}
	if protocol != "tcp" && protocol != "udp" {
		return -1, -1, nil
	}
	if portMin <= 0 && portMax <= 0 {
		return 1, 65535, nil
	}
	if portMin <= 0 {
		portMin = portMax
	}
	if portMax <= 0 {
		portMax = portMin
	}
	if portMin > 65535 || portMax > 65535 {
		return 0, 0, NewCLError(ErrInvalidParameter, "Port out of range, must be 1-65535", nil)
	}
	if portMin > portMax {
		return 0, 0, NewCLError(ErrInvalidParameter, "PortMax should be greater than or equal to PortMin", nil)
	}
	return portMin, portMax, nil
}

// normalizeICMPTypeCode 校验 ICMP type/code：-1 表示任意；type 取 0-254（iptables 把 255 当作任意 type），
// code 取 0-255；只有指定了 type 才能指定 code（--icmp-type 的格式为 type[/code]）
func normalizeICMPTypeCode(icmpType, icmpCode int32) (int32, int32, error) {
	if icmpType < -1 || icmpType > 254 {
		return 0, 0, NewCLError(ErrInvalidParameter, "ICMP type out of range, must be -1 (any) or 0-254", nil)
	}
	if icmpCode < -1 || icmpCode > 255 {
		return 0, 0, NewCLError(ErrInvalidParameter, "ICMP code out of range, must be -1 (any) or 0-255", nil)
	}
	if icmpType == -1 && icmpCode != -1 {
		return 0, 0, NewCLError(ErrInvalidParameter, "ICMP code requires ICMP type", nil)
	}
	return icmpType, icmpCode, nil
}

// isPortProtocol tcp/udp 的 port_min/port_max 是端口范围；icmp 复用为 type/code，gre/ipv6 不使用
func isPortProtocol(protocol string) bool {
	return protocol == "tcp" || protocol == "udp"
}

// checkPayloadPorts 请求中显式传入的 tcp/udp 端口必须在 1-65535（不传表示全部端口或沿用原值），
// 避免 0/-1 被 normalizeRulePorts 当作“未指定”，悄悄变成单端口或全部端口
func checkPayloadPorts(protocol string, portMin, portMax *int32) error {
	if !isPortProtocol(protocol) {
		return nil
	}
	for _, p := range []*int32{portMin, portMax} {
		if p != nil && (*p < 1 || *p > 65535) {
			return NewCLError(ErrInvalidParameter, "TCP/UDP port must be 1-65535", nil)
		}
	}
	return nil
}

// RulePortsFromPayload 解析创建规则请求中的可选端口：未传的记为 -1
// （tcp/udp 两个都未传表示全部端口，只传一个表示单端口；icmp 表示任意 type/code）
func RulePortsFromPayload(protocol string, portMin, portMax *int32) (int32, int32, error) {
	if err := checkPayloadPorts(protocol, portMin, portMax); err != nil {
		return 0, 0, err
	}
	min, max := int32(-1), int32(-1)
	if portMin != nil {
		min = *portMin
	}
	if portMax != nil {
		max = *portMax
	}
	return min, max, nil
}

// resolveRulePorts 合并更新请求中的端口：未传的沿用原值。
// 端口字段含义随协议变化（tcp/udp 端口范围 ↔ icmp type/code）时原值不能沿用：
// 改为 icmp/gre/ipv6 且未传时取 -1（任意）；改为 tcp/udp 时必须显式传端口，不默认放开全部端口
func resolveRulePorts(oldProtocol, newProtocol string, oldMin, oldMax int32, portMin, portMax *int32) (int32, int32, error) {
	if err := checkPayloadPorts(newProtocol, portMin, portMax); err != nil {
		return 0, 0, err
	}
	min, max := oldMin, oldMax
	if isPortProtocol(newProtocol) != isPortProtocol(oldProtocol) {
		if isPortProtocol(newProtocol) && portMin == nil && portMax == nil {
			return 0, 0, NewCLError(ErrInvalidParameter, "port_min/port_max are required when changing protocol to tcp/udp", nil)
		}
		min, max = -1, -1
	}
	if portMin != nil {
		min = *portMin
	}
	if portMax != nil {
		max = *portMax
	}
	return min, max, nil
}

// optionalPort 日志用：未传的端口显示为 nil
func optionalPort(p *int32) string {
	if p == nil {
		return "nil"
	}
	return strconv.Itoa(int(*p))
}

func (a *SecruleAdminService) Update(ctx context.Context, secrule *model.SecurityRule, secgroup *model.SecurityGroup, name, remoteIp, direction, protocol string, portMin, portMax *int32) (err error) {
	logger.Ctx(ctx).Infof("ENTER SecruleAdmin.Update: id=%d, name=%s, remoteIp=%s, direction=%s, protocol=%s, portMin=%s, portMax=%s", secrule.ID, name, remoteIp, direction, protocol, optionalPort(portMin), optionalPort(portMax))
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT SecruleAdmin.Update: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT SecruleAdmin.Update: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckResourceOrg(model.OrgWriter, secrule.Owner)
	if !permit {
		logger.Ctx(ctx).Error("Not authorized for this operation")
		err = NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
		return
	}
	ctx, db := GetContextDB(ctx)
	if remoteIp != "" {
		netLen := strings.Split(remoteIp, "/")
		if len(netLen) != 2 {
			err = NewCLError(ErrInvalidParameter, "Invalid CIDR format for RemoteIp", nil)
			return
		}
		n, _ := strconv.Atoi(netLen[1])
		if n < 0 || n > 32 {
			err = NewCLError(ErrInvalidParameter, "Invalid Netmask for RemoteIp", nil)
			return
		}
		secrule.RemoteIp = remoteIp
	}
	if direction != "" {
		secrule.Direction = direction
	}
	oldProtocol := secrule.Protocol
	if protocol != "" {
		secrule.Protocol = protocol
	}
	if name != "" {
		secrule.Name = name
	}
	// 未传的端口沿用原值（端口字段含义随协议变化时另行处理），再按新协议统一校验
	newMin, newMax, err := resolveRulePorts(oldProtocol, secrule.Protocol, secrule.PortMin, secrule.PortMax, portMin, portMax)
	if err != nil {
		return
	}
	secrule.PortMin, secrule.PortMax, err = normalizeRulePorts(secrule.Protocol, newMin, newMax)
	if err != nil {
		return
	}
	// 修改后与同组另一条规则完全相同时拒绝（规则表有唯一索引兜底，这里给出明确提示）；map 条件避免跳过值为 0 的端口
	duplicate := &model.SecurityRule{}
	if db.Where(map[string]interface{}{"secgroup": secrule.Secgroup, "remote_ip": secrule.RemoteIp, "direction": secrule.Direction, "ip_version": secrule.IpVersion, "protocol": secrule.Protocol, "port_min": secrule.PortMin, "port_max": secrule.PortMax}).Where("id <> ?", secrule.ID).Take(duplicate).Error == nil {
		err = NewCLError(ErrSecurityRuleUpdateFailed, "An identical security rule already exists in this security group", nil)
		return
	}
	err = db.Model(&model.SecurityRule{}).Where("id = ?", secrule.ID).Updates(map[string]interface{}{"name": secrule.Name, "remote_ip": secrule.RemoteIp, "direction": secrule.Direction, "protocol": secrule.Protocol, "port_min": secrule.PortMin, "port_max": secrule.PortMax}).Error
	if err != nil {
		logger.Ctx(ctx).Error("DB failed to save security rule ", err)
		err = NewCLError(ErrSecurityRuleUpdateFailed, "Failed to update security rule", err)
		return
	}
	err = a.ApplySecgroup(ctx, secgroup)
	if err != nil {
		logger.Ctx(ctx).Error("Failed to apply security group", err)
		return
	}
	return
}

func (a *SecruleAdminService) Create(ctx context.Context, name, remoteIp, direction, protocol string, portMin, portMax int32, secgroup *model.SecurityGroup) (secrule *model.SecurityRule, err error) {
	logger.Ctx(ctx).Infof("ENTER SecruleAdmin.Create: name=%s, remoteIp=%s, direction=%s, protocol=%s, portMin=%d, portMax=%d, secgroupID=%d", name, remoteIp, direction, protocol, portMin, portMax, secgroup.ID)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT SecruleAdmin.Create: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT SecruleAdmin.Create: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckResourceOrg(model.OrgWriter, secgroup.Owner)
	if !permit {
		logger.Ctx(ctx).Error("Not authorized for this operation")
		err = NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
		return
	}
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	portMin, portMax, err = normalizeRulePorts(protocol, portMin, portMax)
	if err != nil {
		return
	}
	// 已有相同规则：返回已有记录（幂等）。默认规则等内部调用依赖“已存在即成功”，API 也需要规则对象组装响应。
	// 必须用 map 条件：结构体条件会跳过零值字段，ICMP type/code 为 0 时会匹配到别的规则
	existing := &model.SecurityRule{}
	if db.Where(map[string]interface{}{"secgroup": secgroup.ID, "remote_ip": remoteIp, "direction": direction, "ip_version": "ipv4", "protocol": protocol, "port_min": portMin, "port_max": portMax}).Take(existing).Error == nil {
		logger.Ctx(ctx).Infof("Existing rule %s %s %s %d %d for security group %d, reuse it", remoteIp, direction, protocol, portMin, portMax, secgroup.ID)
		secrule = existing
		return
	}
	secrule = &model.SecurityRule{
		Model:     model.Model{Creater: memberShip.UserID},
		Owner:     memberShip.OrgID,
		Secgroup:  secgroup.ID,
		RemoteIp:  remoteIp,
		Direction: direction,
		IpVersion: "ipv4",
		Protocol:  protocol,
		PortMin:   portMin,
		PortMax:   portMax,
		Name:      name,
	}
	err = db.Create(secrule).Error
	if err != nil {
		logger.Ctx(ctx).Error("DB failed to create security rule", err)
		err = NewCLError(ErrSecurityRuleCreateFailed, "Failed to create security rule", err)
		return
	}
	err = a.ApplySecgroup(ctx, secgroup)
	if err != nil {
		logger.Ctx(ctx).Error("Failed to apply security rule", err)
		return
	}
	return
}

func (a *SecruleAdminService) Delete(ctx context.Context, secrule *model.SecurityRule, secgroup *model.SecurityGroup) (err error) {
	logger.Ctx(ctx).Infof("ENTER SecruleAdmin.Delete: secruleID=%d, secgroupID=%d", secrule.ID, secgroup.ID)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT SecruleAdmin.Delete: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT SecruleAdmin.Delete: success")
		}
	}()
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckResourceOrg(model.OrgWriter, secrule.Owner)
	if !permit {
		logger.Ctx(ctx).Error("Not authorized to delete the router")
		err = NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
		return
	}
	if err = db.Delete(secrule).Error; err != nil {
		logger.Ctx(ctx).Error("DB failed to delete security rule, %v", err)
		err = NewCLError(ErrSecurityRuleDeleteFailed, "Failed to delete security rule", err)
		return
	}
	err = a.ApplySecgroup(ctx, secgroup)
	if err != nil {
		logger.Ctx(ctx).Error("Failed to apply security rule", err)
		return
	}
	return
}

func (a *SecruleAdminService) List(ctx context.Context, offset, limit int64, order string, secgroup *model.SecurityGroup) (total int64, secrules []*model.SecurityRule, err error) {
	logger.Ctx(ctx).Infof("ENTER SecruleAdmin.List: offset=%d, limit=%d, order=%s, secgroupID=%d", offset, limit, order, secgroup.ID)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT SecruleAdmin.List: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT SecruleAdmin.List: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckResourceOrg(model.OrgReader, secgroup.Owner)
	if !permit {
		logger.Ctx(ctx).Error("Not authorized for this operation")
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

	query, args := memberShip.GetOrgFilter()
	secrules = []*model.SecurityRule{}
	if err = db.Model(&model.SecurityRule{}).Where("secgroup = ?", secgroup.ID).Where(query, args...).Count(&total).Error; err != nil {
		logger.Ctx(ctx).Error("DB failed to count security rule(s), %v", err)
		err = NewCLError(ErrSQLSyntaxError, "Failed to count security rule(s)", err)
		return
	}
	db = dbs.Sortby(db.Offset(int(offset)).Limit(int(limit)), order)
	if err = db.Where("secgroup = ?", secgroup.ID).Where(query, args...).Find(&secrules).Error; err != nil {
		logger.Ctx(ctx).Error("DB failed to query security rule(s), %v", err)
		err = NewCLError(ErrSQLSyntaxError, "Failed to query security rule(s)", err)
		return
	}

	return
}

func (a *SecruleAdminService) Get(ctx context.Context, id int64, secgroup *model.SecurityGroup) (secrule *model.SecurityRule, err error) {
	logger.Ctx(ctx).Infof("ENTER SecruleAdmin.Get: id=%d, secgroupID=%d", id, secgroup.ID)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT SecruleAdmin.Get: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT SecruleAdmin.Get: success")
		}
	}()
	if id <= 0 {
		err = fmt.Errorf("Invalid security rule ID: %d", id)
		logger.Ctx(ctx).Error(err)
		return
	}
	memberShip := GetMemberShip(ctx)
	query, args := memberShip.GetOrgFilter()
	db := DB()
	secrule = &model.SecurityRule{Model: model.Model{ID: id}}
	err = db.Where(query, args...).Take(secrule).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to query secrule", err)
		return
	}
	permit := memberShip.CheckResourceOrg(model.OrgReader, secrule.Owner)
	if !permit {
		logger.Ctx(ctx).Error("Not authorized to get security group")
		err = fmt.Errorf("Not authorized")
		return
	}
	return
}

func (a *SecruleAdminService) GetSecruleByUUID(ctx context.Context, uuID string, secgroup *model.SecurityGroup) (secrule *model.SecurityRule, err error) {
	logger.Ctx(ctx).Infof("ENTER SecruleAdmin.GetSecruleByUUID: uuID=%s, secgroupID=%d", uuID, secgroup.ID)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT SecruleAdmin.GetSecruleByUUID: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT SecruleAdmin.GetSecruleByUUID: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	query, args := memberShip.GetOrgFilter()
	db := DB()
	secrule = &model.SecurityRule{}
	err = db.Where(query, args...).Where("uuid = ? and secgroup = ?", uuID, secgroup.ID).Take(secrule).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to query secrule", err)
		return
	}
	permit := memberShip.CheckResourceOrg(model.OrgReader, secrule.Owner)
	if !permit {
		logger.Ctx(ctx).Error("Not authorized to get security group")
		err = fmt.Errorf("Not authorized")
		return
	}
	return
}
