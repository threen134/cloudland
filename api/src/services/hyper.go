/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

*/

package services

import (
	"context"
	"fmt"
	"os"
	"strings"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"
)

// clandPubKeyPath is the path inside the clapi container where cland.key.pub
// is mounted (see docker-compose.yml: ../.ssh:/opt/cloudland/deploy/.ssh:ro).
const clandPubKeyPath = "/opt/cloudland/deploy/.ssh/cland.key.pub"

var (
	hyperAdmin = &HyperAdmin{}
)

type HyperAdmin struct{}

// GetHyperNames 返回 hostid -> hostname 映射。节点表很小，一次取回即可，
// 供需要把节点编号显示成名字的接口使用（如迁移列表的源/目标节点）
func (a *HyperAdmin) GetHyperNames(ctx context.Context) (names map[int32]string, err error) {
	ctx, db := GetContextDB(ctx)
	hypers := []*model.Hyper{}
	if err = db.Where("hostid >= 0").Find(&hypers).Error; err != nil {
		logger.Ctx(ctx).Errorf("Failed to query hypervisors: %v", err)
		return nil, NewCLError(ErrSQLSyntaxError, "Failed to query hypervisors", err)
	}
	names = make(map[int32]string, len(hypers))
	for _, h := range hypers {
		names[h.Hostid] = h.Hostname
	}
	return names, nil
}

// GetInstanceCounts 统计每个节点上的虚拟机数量（已软删除的不计），一次分组查询返回
// hostid -> 数量，供节点列表展示，避免前端拉全量实例再分组（实例接口默认分页 50 条会漏数）
func (a *HyperAdmin) GetInstanceCounts(ctx context.Context) (counts map[int32]int64, err error) {
	ctx, db := GetContextDB(ctx)
	rows := []struct {
		Hyper int32
		Count int64
	}{}
	if err = db.Model(&model.Instance{}).Select("hyper, count(*) as count").
		Where("hyper >= 0").Group("hyper").Scan(&rows).Error; err != nil {
		logger.Ctx(ctx).Errorf("Failed to count instances per hypervisor: %v", err)
		return nil, NewCLError(ErrSQLSyntaxError, "Failed to count instances per hypervisor", err)
	}
	counts = make(map[int32]int64, len(rows))
	for _, r := range rows {
		counts[r.Hyper] = r.Count
	}
	return counts, nil
}

func (a *HyperAdmin) List(ctx context.Context, offset, limit int64, order, query string) (total int64, hypers []*model.Hyper, err error) {
	logger.Ctx(ctx).Infof("ENTER HyperAdmin.List: offset=%d, limit=%d, order=%s, query=%s", offset, limit, order, query)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT HyperAdmin.List: error=%v", err)
		} else {
			logger.Ctx(ctx).Infof("EXIT HyperAdmin.List: total=%d, count=%d", total, len(hypers))
		}
	}()
	ctx, db := GetContextDB(ctx)
	if limit == 0 {
		limit = 16
	}

	if order == "" {
		order = "hostid"
	}

	hypers = []*model.Hyper{}
	if err = db.Model(&model.Hyper{}).Where("hostid >= 0").Scopes(dbs.Contains(query, "hostname")).Count(&total).Error; err != nil {
		return 0, nil, NewCLError(ErrSQLSyntaxError, "Failed to count hypervisors", err)
	}
	db = dbs.Sortby(db.Offset(int(offset)).Limit(int(limit)), order)
	if err = db.Preload("Zone").Where("hostid >= 0").Scopes(dbs.Contains(query, "hostname")).Find(&hypers).Error; err != nil {
		return 0, nil, NewCLError(ErrSQLSyntaxError, "Failed to retrieve hypervisors", err)
	}
	// GORM 链式调用复用同一 Statement，沿用 db 会带上 Preload 与过滤条件，取新会话再查关联记录
	_, db = GetContextDB(ctx)
	for _, hyper := range hypers {
		hyper.Resource = &model.Resource{}
		if lerr := db.Where("hostid = ?", hyper.Hostid).Take(hyper.Resource).Error; lerr != nil {
			logger.Ctx(ctx).Warningf("Hypervisor %s (hostid: %d) has no associated resource record: %+v", hyper.Hostname, hyper.Hostid, lerr)
		}
	}

	return
}

func (a *HyperAdmin) GetHyperByUUID(ctx context.Context, uuid string) (hyper *model.Hyper, err error) {
	logger.Ctx(ctx).Infof("ENTER HyperAdmin.GetHyperByUUID: uuid=%s", uuid)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT HyperAdmin.GetHyperByUUID: error=%v", err)
		} else {
			logger.Ctx(ctx).Infof("EXIT HyperAdmin.GetHyperByUUID: success, hostID=%d", hyper.Hostid)
		}
	}()
	_, db := GetContextDB(ctx)
	hyper = &model.Hyper{}
	if err = db.Preload("Zone").Where("uuid = ?", uuid).Take(hyper).Error; err != nil {
		logger.Ctx(ctx).Error("Failed to query hypervisor by UUID", err)
		return nil, NewCLError(ErrHypervisorNotFound, "Specified hypervisor not found", err)
	}
	hyper.Resource = &model.Resource{}
	if lerr := db.Where("hostid = ?", hyper.Hostid).Take(hyper.Resource).Error; lerr != nil {
		logger.Ctx(ctx).Warningf("Hypervisor %s (hostid: %d, uuid: %s) has no associated resource record: %+v", hyper.Hostname, hyper.Hostid, hyper.UUID, lerr)
	}
	return
}

func (a *HyperAdmin) SetStatus(ctx context.Context, hostID int32, status int32) (err error) {
	logger.Ctx(ctx).Infof("ENTER HyperAdmin.SetStatus: hostID=%d, status=%d", hostID, status)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT HyperAdmin.SetStatus: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT HyperAdmin.SetStatus: success")
		}
	}()
	hyper, err := a.GetHyperByHostid(ctx, hostID)
	if err != nil {
		return
	}
	if hyper.Status == status {
		return nil // No change needed
	}
	hyper.Status = status
	return a.Update(ctx, hyper)
}

// Update function is used to:
// 1. set the hypervisor status active or disabled
// 2. modify the hypervisor remark
// 3. modify the zone of the hypervisor
// 4. modify the over commit rates of hypervisor
func (a *HyperAdmin) Update(ctx context.Context, hyper *model.Hyper) (err error) {
	logger.Ctx(ctx).Infof("ENTER HyperAdmin.Update: hostID=%d, status=%d, zoneID=%d", hyper.Hostid, hyper.Status, hyper.ZoneID)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT HyperAdmin.Update: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT HyperAdmin.Update: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckSystemPermission()
	if !permit {
		err = NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
		logger.Ctx(ctx).Error("Not authorized for this operation", err)
		return
	}
	ctx, db := GetContextDB(ctx)
	hyperInDB := &model.Hyper{}
	hyperInDB.ID = hyper.ID
	if err = db.Preload("Zone").Take(hyperInDB).Error; err != nil {
		logger.Ctx(ctx).Error("Specified hypervisor not found", err)
		return NewCLError(ErrHypervisorNotFound, "Specified hypervisor not found", err)
	}
	// Update the hypervisor status, remark, or zone
	callScript := false
	restartCloudlet := 0
	if hyper.Status != hyperInDB.Status {
		logger.Ctx(ctx).Info("Updating hypervisor status from", hyperInDB.GetStatus(), "to", hyper.GetStatus())
		hyperInDB.Status = hyper.Status
		callScript = true
	}
	// update remark
	hyperInDB.Remark = hyper.Remark
	if hyper.ZoneID != hyperInDB.ZoneID {
		logger.Ctx(ctx).Info("Updating hypervisor zone from", hyperInDB.ZoneID, "to", hyper.ZoneID)
		zone, err := zoneAdmin.Get(ctx, hyper.ZoneID)
		if err != nil {
			logger.Ctx(ctx).Errorf("Failed to get zone(%d), %+v", hyper.ZoneID, err)
			return err
		}
		hyperInDB.Zone = zone
		hyperInDB.ZoneID = zone.ID
		callScript = true
		restartCloudlet = 1
	}
	// update over commit rates
	if hyper.CpuOverRate != hyperInDB.CpuOverRate {
		logger.Ctx(ctx).Info("Updating hypervisor CPU over commit rate from", hyperInDB.CpuOverRate, "to", hyper.CpuOverRate)
		hyperInDB.CpuOverRate = hyper.CpuOverRate
		callScript = true
	}
	if hyper.MemOverRate != hyperInDB.MemOverRate {
		logger.Ctx(ctx).Info("Updating hypervisor memory over commit rate from", hyperInDB.MemOverRate, "to", hyper.MemOverRate)
		hyperInDB.MemOverRate = hyper.MemOverRate
		callScript = true
	}
	if hyper.DiskOverRate != hyperInDB.DiskOverRate {
		logger.Ctx(ctx).Info("Updating hypervisor disk over commit rate from", hyperInDB.DiskOverRate, "to", hyper.DiskOverRate)
		hyperInDB.DiskOverRate = hyper.DiskOverRate
		callScript = true
	}
	if err = db.Save(hyperInDB).Error; err != nil {
		logger.Ctx(ctx).Error("Failed to update hypervisor", err)
		return
	}
	if callScript {
		logger.Ctx(ctx).Info("Calling script to update hypervisor status")
		// Call the script to update hypervisor status
		control := fmt.Sprintf("inter=%d", hyperInDB.Hostid)
		command := fmt.Sprintf("/opt/cloudland/scripts/backend/update_hyper.sh '%d' '%s' '%d' '%f' '%f' '%f'",
			hyperInDB.Status, ShellEscape(hyperInDB.Zone.Name), restartCloudlet, hyperInDB.CpuOverRate, hyperInDB.MemOverRate, hyperInDB.DiskOverRate)
		err = HyperExecute(ctx, control, command)
		if err != nil {
			logger.Ctx(ctx).Errorf("Failed to call script update hyper %+v", err)
			return
		}
		logger.Ctx(ctx).Infof("Successfully updated hypervisor %d via script", hyperInDB.Hostid)
	}
	return
}

func (a *HyperAdmin) GetHyperByHostid(ctx context.Context, hostid int32) (hyper *model.Hyper, err error) {
	logger.Ctx(ctx).Infof("ENTER HyperAdmin.GetHyperByHostid: hostid=%d", hostid)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT HyperAdmin.GetHyperByHostid: error=%v", err)
		} else {
			logger.Ctx(ctx).Infof("EXIT HyperAdmin.GetHyperByHostid: success, hostname=%s", hyper.Hostname)
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckSystemPermission()
	if !permit {
		err = NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
		logger.Ctx(ctx).Error("Not authorized for this operation", err)
		return
	}
	_, db := GetContextDB(ctx)
	hyper = &model.Hyper{}
	if err = db.Preload("Zone").Where("hostid = ?", hostid).Take(hyper).Error; err != nil {
		logger.Ctx(ctx).Error("Failed to query hypervisor", err)
		return nil, NewCLError(ErrHypervisorNotFound, "Specified hypervisor not found", err)
	}

	// Load resource information
	hyper.Resource = &model.Resource{}
	if lerr := db.Where("hostid = ?", hyper.Hostid).Take(hyper.Resource).Error; lerr != nil {
		logger.Ctx(ctx).Warningf("Hypervisor %s (hostid: %d) has no associated resource record: %+v", hyper.Hostname, hyper.Hostid, lerr)
		// If no resource record, initialize with defaults
		hyper.Resource = &model.Resource{
			Hostid: hyper.Hostid,
		}
	}
	return
}

func (a *HyperAdmin) GetHyperByHostname(ctx context.Context, hostname string) (hyper *model.Hyper, err error) {
	logger.Ctx(ctx).Infof("ENTER HyperAdmin.GetHyperByHostname: hostname=%s", hostname)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT HyperAdmin.GetHyperByHostname: error=%v", err)
		} else {
			logger.Ctx(ctx).Infof("EXIT HyperAdmin.GetHyperByHostname: success, hostID=%d", hyper.Hostid)
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckSystemPermission()
	if !permit {
		err = NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
		logger.Ctx(ctx).Error("Not authorized for this operation", err)
		return
	}
	ctx, db := GetContextDB(ctx)
	hyper = &model.Hyper{}
	if err = db.Where("hostname = ?", hostname).Take(hyper).Error; err != nil {
		logger.Ctx(ctx).Error("Failed to query hypervisor", err)
		return nil, NewCLError(ErrHypervisorNotFound, "Specified hypervisor not found", err)
	}
	return
}

func (a *HyperAdmin) AllocateHostID(ctx context.Context) (hostID int32, err error) {
	logger.Ctx(ctx).Infof("ENTER HyperAdmin.AllocateHostID")
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT HyperAdmin.AllocateHostID: error=%v", err)
		} else {
			logger.Ctx(ctx).Infof("EXIT HyperAdmin.AllocateHostID: hostID=%d", hostID)
		}
	}()
	_, db := GetContextDB(ctx)
	var maxID int32
	if err = db.Model(&model.Hyper{}).Select("max(hostid)").Row().Scan(&maxID); err != nil {
		// If no records exist, Row().Scan might return an error or maxID will be 0.
		// We ensure it starts from 1 if it's currently unset/0.
		logger.Ctx(ctx).Info("No existing hypervisors found, starting hostID from 1")
		return 1, nil // Start from 1 if no records or error
	}
	// If maxID is 0 (e.g., table is empty and max() returns 0), start from 1.
	// Otherwise, increment the maxID found.
	if maxID < 1 {
		hostID = 1
	} else {
		hostID = maxID + 1
	}
	return hostID, nil
}

func (a *HyperAdmin) Deploy(ctx context.Context, ip, hostname, networkDevice, vlanDevice, privateVlanDevice, dnsServer, domain, zoneName, virtType string) (hyper *model.Hyper, deployCmd string, err error) {
	logger.Ctx(ctx).Infof("ENTER HyperAdmin.Deploy: ip=%s, hostname=%s, zone=%s", ip, hostname, zoneName)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT HyperAdmin.Deploy: error=%v", err)
		} else {
			logger.Ctx(ctx).Infof("EXIT HyperAdmin.Deploy: success, hostID=%d", hyper.Hostid)
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckSystemPermission()
	if !permit {
		return nil, "", NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
	}
	ctx, db := GetContextDB(ctx)

	// Check if hostname already exists
	var hostID int32
	existing := &model.Hyper{}
	if err = db.Preload("Zone").Where("hostname = ?", hostname).Take(existing).Error; err == nil {
		// If exists, only allow retry if status is deploying or failed
		if existing.Status == 4 || existing.Status == 5 { // HYPER_DEPLOYING or HYPER_DEPLOY_FAILED
			logger.Ctx(ctx).Infof("Retrying deployment for existing hypervisor: %s (HostID: %d, NewIP: %s)", hostname, existing.Hostid, ip)
			hostID = existing.Hostid
			hyper = existing

			if zoneName != "" {
				var zone *model.Zone
				zone, err = zoneAdmin.GetZoneByName(ctx, zoneName)
				if err != nil {
					return nil, "", NewCLError(ErrZoneNotFound, fmt.Sprintf("Zone '%s' not found, please check your zone information", zoneName), err)
				}
				hyper.ZoneID = zone.ID
				hyper.Zone = zone
			} else if hyper.Zone != nil {
				zoneName = hyper.Zone.Name
			}

			hyper.HostIP = ip
			hyper.VirtType = virtType
			hyper.Status = 4 // Reset to deploying
			if err = db.Save(hyper).Error; err != nil {
				return nil, "", NewCLError(ErrSQLSyntaxError, "Failed to update existing hypervisor record", err)
			}
			RegisterHostInDns(hostname, ip)
		} else {
			return nil, "", NewCLError(ErrHypervisorInvalidState, "Hypervisor with this hostname already exists and is not in a retryable state", nil)
		}
	} else {
		// Allocate unique host ID for new hypervisor
		hostID, err = a.AllocateHostID(ctx)
		if err != nil {
			return nil, "", err
		}

		// Create hyper record in deploying state
		var zone *model.Zone
		if zoneName != "" {
			zone, err = zoneAdmin.GetZoneByName(ctx, zoneName)
			if err != nil {
				return nil, "", NewCLError(ErrZoneNotFound, fmt.Sprintf("Zone '%s' not found, please check your zone information", zoneName), err)
			}
		} else {
			zone, err = zoneAdmin.GetDefaultZone(ctx)
			if err != nil {
				return nil, "", NewCLError(ErrZoneNotFound, "No zone specified and no default zone found in system", err)
			}
		}
		hyper = &model.Hyper{
			Hostid:   hostID,
			Hostname: hostname,
			HostIP:   ip,
			Status:   4, // HYPER_DEPLOYING
			VirtType: virtType,
			ZoneID:   zone.ID,
			Zone:     zone,
		}
		if err = db.Create(hyper).Error; err != nil {
			return nil, "", NewCLError(ErrSQLSyntaxError, "Failed to create hypervisor record", err)
		}
		logger.Ctx(ctx).Infof("Created new hypervisor record for %s (HostID: %d, IP: %s, Zone: %s)", hostname, hostID, ip, zoneName)
		RegisterHostInDns(hostname, ip)
	}

	// Construct deploy command
	controllerIP := os.Getenv("MANAGEMENT_VIP")
	if controllerIP == "" {
		controllerIP = os.Getenv("PUBLIC_IP")
	}
	if controllerIP == "" {
		controllerIP = "127.0.0.1"
	}
	// 计算节点与控制面使用同一代码分支（REPO_BRANCH 由控制面部署脚本写入 .env）
	repoBranch := os.Getenv("REPO_BRANCH")
	if repoBranch == "" {
		repoBranch = "staging"
	} else if strings.ContainsAny(repoBranch, "'\"`$\\ \t\n\r;&|") {
		logger.Ctx(ctx).Warningf("REPO_BRANCH %q contains unsafe characters; deploy_command falls back to staging", repoBranch)
		repoBranch = "staging"
	}
	deployScriptURL := os.Getenv("DEPLOY_SCRIPT_URL")
	if deployScriptURL == "" {
		deployScriptURL = fmt.Sprintf("https://raw.githubusercontent.com/threen134/cloudland/%s/deploy/docker/scripts/deploy-compute-node.sh", repoBranch)
	}

	// Embed cland.key.pub into the deploy command so the compute node trusts the
	// control plane's SSH key from its first deployment, before `cloudlet-go node-add`
	// distributes the private key.
	pubKeyExport := ""
	if data, readErr := os.ReadFile(clandPubKeyPath); readErr == nil {
		pubKey := strings.TrimSpace(string(data))
		switch {
		case pubKey == "":
			logger.Ctx(ctx).Warningf("cland.key.pub at %s is empty; deploy_command will omit CLAND_PUBKEY", clandPubKeyPath)
		case strings.ContainsAny(pubKey, "'\n\r"):
			// Sanity check: standard ssh-rsa/ed25519 pubkeys never contain single
			// quotes or newlines. If they do, the comment field has been hand-edited
			// in a way that would break our single-quoted shell embed. Refuse to
			// embed and let the operator fix the key, rather than emitting a broken
			// deploy_command.
			logger.Ctx(ctx).Warningf("cland.key.pub at %s contains unsafe characters (quote/newline); deploy_command will omit CLAND_PUBKEY", clandPubKeyPath)
		default:
			pubKeyExport = fmt.Sprintf("CLAND_PUBKEY='%s' ", pubKey)
		}
	} else {
		logger.Ctx(ctx).Warningf("cannot read cland.key.pub at %s: %v; deploy_command will omit CLAND_PUBKEY", clandPubKeyPath, readErr)
	}

	// cloudlet-go authenticates to cland with the same shared token as clapi.
	tokenExport := ""
	if token := ClandToken(); token != "" {
		if strings.ContainsAny(token, "'\n\r") {
			logger.Ctx(ctx).Warning("GRPC_AUTH_TOKEN contains unsafe characters (quote/newline); deploy_command will omit it")
		} else {
			tokenExport = fmt.Sprintf("GRPC_AUTH_TOKEN='%s' ", token)
		}
	}

	// 用 --preserve-env 显式列出变量：Ubuntu 26.04 默认的 sudo-rs 会忽略 sudo -E；
	// 变量经 export 传入而非放在命令参数里，令牌不会出现在进程列表中
	deployEnvVars := "CLAND_PUBKEY,GRPC_AUTH_TOKEN,CONTROLLER_IP,HOSTNAME,NETWORK_DEVICE,VLAN_DEVICE,PRIVATE_VLAN_DEVICE,DNS_SERVER,NODE_ID,DOMAIN,ZONE_NAME,VIRT_TYPE,REPO_BRANCH"
	deployCmd = fmt.Sprintf(
		"export %s%sCONTROLLER_IP=%s HOSTNAME=%s NETWORK_DEVICE=%s VLAN_DEVICE=%s PRIVATE_VLAN_DEVICE=%s DNS_SERVER=%s NODE_ID=%d DOMAIN=%s ZONE_NAME=%s VIRT_TYPE=%s REPO_BRANCH=%s; "+
			"curl -sSL %s | sudo --preserve-env=%s bash",
		pubKeyExport, tokenExport, controllerIP, hostname, networkDevice, vlanDevice, privateVlanDevice, dnsServer, hostID, domain, zoneName, virtType, repoBranch,
		deployScriptURL, deployEnvVars,
	)

	return hyper, deployCmd, nil
}

func (a *HyperAdmin) Maintain(ctx context.Context, hostID int32, migrate bool, targetHyper int32) (err error) {
	logger.Ctx(ctx).Infof("ENTER HyperAdmin.Maintain: hostID=%d, migrate=%v, targetHyper=%d", hostID, migrate, targetHyper)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT HyperAdmin.Maintain: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT HyperAdmin.Maintain: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckSystemPermission()
	if !permit {
		return NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
	}
	ctx, db := GetContextDB(ctx)

	hyper := &model.Hyper{}
	if err = db.Where("hostid = ?", hostID).Take(hyper).Error; err != nil {
		return NewCLError(ErrHypervisorNotFound, "Specified hypervisor not found", err)
	}
	if hyper.Status != 1 && hyper.Status != 0 {
		return NewCLError(ErrHypervisorInvalidState, "Hypervisor must be active or disabled to maintain", nil)
	}

	// 有虚拟机正在迁入时拒绝进入维护：这些虚拟机此刻的 instances.hyper 仍指向源节点，
	// 不会出现在下面的腾空快照里，等迁移完成就会落到一台已处于维护中的节点上并留在那儿
	// （腾空是一次性快照，不会持续收敛）。宁可让运维等迁移结束再重试，也不要中止迁移——
	// 大磁盘的迁移动辄数分钟，废掉代价太大
	var incoming int64
	if err = db.Model(&model.Migration{}).Where("target_hyper = ? and status in ?", hostID,
		[]string{"in_progress", "target_prepared", "source_prepared"}).Count(&incoming).Error; err != nil {
		return NewCLError(ErrSQLSyntaxError, "Failed to count incoming migrations", err)
	}
	if incoming > 0 {
		logger.Ctx(ctx).Errorf("Hypervisor %d has %d incoming migration(s), refusing maintenance", hostID, incoming)
		return NewCLError(ErrHypervisorInvalidState,
			fmt.Sprintf("%d instance(s) are migrating to this hypervisor, please retry after they finish", incoming), nil)
	}

	originalStatus := hyper.Status
	// Set status to maintaining
	if err = db.Model(hyper).Update("status", 2).Error; err != nil {
		return NewCLError(ErrSQLSyntaxError, "Failed to update hypervisor status", err)
	}
	// 后续失败时把节点状态改回去，否则节点停在"维护中"。心跳上报恰好会把它覆盖回来，
	// 但那是巧合，不能依赖
	defer func() {
		if err != nil {
			if rerr := db.Model(hyper).Update("status", originalStatus).Error; rerr != nil {
				logger.Ctx(ctx).Errorf("Failed to restore hypervisor status after maintenance failure: %v", rerr)
			}
		}
	}()

	if migrate {
		// Find instances on this hyper
		// 必须预加载 Volumes：迁移要用引导卷判断存储类型，不加载则 migrationAdmin.Create
		// 一律报 "Instance has no boot volume"，维护模式的自动迁移对任何实例都无法成功
		instances := []*model.Instance{}
		if err = db.Preload("Volumes").Where("hyper = ?", hostID).Find(&instances).Error; err != nil {
			return NewCLError(ErrSQLSyntaxError, "Failed to query instances", err)
		}

		if len(instances) > 0 {
			// Migrate all instances
			_, err = migrationAdmin.Create(ctx, fmt.Sprintf("maintenance-hyper-%d", hostID), instances, false, targetHyper)
			if err != nil {
				logger.Ctx(ctx).Errorf("Failed to create migrations for maintenance: %v", err)
				return err
			}
		}
	}

	logger.Ctx(ctx).Infof("Hypervisor %d entered maintenance mode (status=%d, migrate=%v, target=%d)", hostID, hyper.Status, migrate, targetHyper)
	return nil
}

func (a *HyperAdmin) Delete(ctx context.Context, hostID int32) (err error) {
	logger.Ctx(ctx).Infof("ENTER HyperAdmin.Delete: hostID=%d", hostID)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT HyperAdmin.Delete: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT HyperAdmin.Delete: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckSystemPermission()
	if !permit {
		return NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
	}
	ctx, db := GetContextDB(ctx)

	hyper := &model.Hyper{}
	if err = db.Where("hostid = ?", hostID).Take(hyper).Error; err != nil {
		return NewCLError(ErrHypervisorNotFound, "Specified hypervisor not found", err)
	}

	// Only allow deletion if there are no instances
	var count int64
	if err = db.Model(&model.Instance{}).Where("hyper = ?", hostID).Count(&count).Error; err != nil {
		return NewCLError(ErrSQLSyntaxError, "Failed to count instances", err)
	}
	if count > 0 {
		return NewCLError(ErrHypervisorInvalidState, fmt.Sprintf("Hypervisor still has %d instances, cannot delete", count), nil)
	}

	// Clean up related records
	db.Where("hostid = ?", hostID).Delete(&model.Resource{})

	// Release RouteIP (system interface)
	ifaces := []*model.Interface{}
	if err = db.Where("hyper = ? AND type = 'system'", hostID).Find(&ifaces).Error; err != nil {
		logger.Ctx(ctx).Errorf("Failed to query system interfaces for hypervisor %d: %v", hostID, err)
	} else if len(ifaces) > 0 {
		if err = DeallocateAddress(ctx, ifaces); err != nil {
			logger.Ctx(ctx).Errorf("Failed to deallocate system address for hypervisor %d: %v", hostID, err)
		}
		if err = db.Where("hyper = ? AND type = 'system'", hostID).Delete(&model.Interface{}).Error; err != nil {
			logger.Ctx(ctx).Errorf("Failed to delete system interfaces for hypervisor %d: %v", hostID, err)
		}
	}

	if err = db.Delete(hyper).Error; err != nil {
		return NewCLError(ErrSQLSyntaxError, "Failed to delete hypervisor record", err)
	}

	// Disconnect the node from cland whatever its status: a node still deploying may already
	// have a connected cloudlet. This runs after the row is gone so a reconnecting cloudlet
	// fails verification. The failure is logged with a local variable so it does not turn
	// the completed deletion into an error.
	if removeErr := NodeRemove(hostID); removeErr != nil {
		logger.Ctx(ctx).Errorf("cland NodeRemove failed for hostid=%d hostname=%s: %v — the cloudlet keeps its current connection but cannot reconnect", hostID, hyper.Hostname, removeErr)
	}

	RemoveHostFromDns(hyper.Hostname)
	logger.Ctx(ctx).Infof("Hypervisor %d deleted successfully from database (Hostname: %s)", hostID, hyper.Hostname)
	return nil
}
