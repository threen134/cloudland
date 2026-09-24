/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

*/

package services

import (
	. "api/src/common"
	"api/src/dbs"
	"api/src/model"
	"api/src/utils/encrpt"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var (
	instanceAdmin = &InstanceAdmin{}
)

const MaxmumSnapshot = 64

type InstanceAdmin struct{}

func generateRandomPassword(length int) (string, error) {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%&*"
	password := make([]byte, length)
	for i := range password {
		idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		if err != nil {
			return "", err
		}
		password[i] = charset[idx.Int64()]
	}
	return string(password), nil
}

type ExecutionCommand struct {
	Control string
	Command string
}

type NetworkLink struct {
	MacAddr string `json:"ethernet_mac_address"`
	Mtu     uint   `json:"mtu"`
	ID      string `json:"id"`
	Type    string `json:"type,omitempty"`
}

type VolumeInfo struct {
	ID      int64  `json:"id"`
	UUID    string `json:"uuid"`
	Device  string `json:"device"`
	Booting bool   `json:"booting"`
}

type InstanceData struct {
	Userdata       string             `json:"userdata"`
	UserdataType   string             `json:"userdata_type"`
	Vendordata     string             `json:"vendordata"`
	VendordataType string             `json:"vendordata_type"`
	DNS            string             `json:"dns"`
	Vlans          []*VlanInfo        `json:"vlans"`
	Networks       []*InstanceNetwork `json:"networks"`
	Links          []*NetworkLink     `json:"links"`
	Volumes        []*VolumeInfo      `json:"volumes"`
	Keys           []string           `json:"keys"`
	RootPasswd     string             `json:"root_passwd"`
	LoginPort      int                `json:"login_port"`
	OSCode         string             `json:"os_code"`
}

type InstancesData struct {
	Instances []*model.Instance `json:"instancedata"`
	IsAdmin   bool              `json:"is_admin"`
}

func (a *InstanceAdmin) Create(ctx context.Context, count int, prefix, userdata string, userdataType string, vendorData string, vendorDataType string, image *model.Image,
	zone *model.Zone, routerID int64, primaryIface *InterfaceInfo, secondaryIfaces []*InterfaceInfo,
	keys []*model.Key, rootPasswd string, loginPort, hyperID int, cpu int32, memory int32, disk int32, nestedEnable bool, bootPool *model.StoragePool) (instances []*model.Instance, err error) {
	logger.Ctx(ctx).Infof("ENTER InstanceAdmin.Create: count=%d, prefix=%s, image=%s, zone=%s, routerID=%d", count, prefix, image.Name, zone.Name, routerID)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT InstanceAdmin.Create: error=%v", err)
		} else {
			logger.Ctx(ctx).Infof("EXIT InstanceAdmin.Create: createdCount=%d", len(instances))
		}
	}()
	if count > 1 && len(primaryIface.PublicIps) > 0 {
		err = NewCLError(ErrInvalidParameter, "Public addresses are not allowed to set when count > 1", nil)
		return
	}
	var execCommands []*ExecutionCommand
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
		if err == nil {
			a.executeCommandList(ctx, execCommands)
		}
	}()
	memberShip := GetMemberShip(ctx)
	if image.Status != "available" {
		err = NewCLError(ErrImageNotAvailable, "Image status not available", nil)
		logger.Ctx(ctx).Error("Image status not available")
		return
	}
	if image.Size > int64(disk)*1024*1024*1024 {
		err = NewCLError(ErrDiskTooSmall, "Flavor disk size is not enough for the image", nil)
		logger.Ctx(ctx).Error(err)
		return
	}
	zoneID := zone.ID
	if hyperID >= 0 {
		if !memberShip.CheckSystemPermission() {
			err = NewCLError(ErrPermissionDenied, "Only system admins can specify a hypervisor", nil)
			return
		}
		hyper := &model.Hyper{}
		err = db.Where("hostid = ?", hyperID).Take(hyper).Error
		if err != nil {
			logger.Ctx(ctx).Error("Failed to query hypervisor", err)
			return nil, NewCLError(ErrHypervisorNotFound, "Failed to find the specified hypervisor", err)
		}
		if hyper.ZoneID != zone.ID {
			logger.Ctx(ctx).Errorf("Hypervisor %v is not in zone %d, %v", hyper, zoneID, err)
			err = NewCLError(ErrInvalidParameter, "Hypervisor is not in this zone", nil)
			return
		}
	}
	if loginPort <= 0 {
		switch image.OSCode {
		case "linux":
			loginPort = 22
		case "windows":
			loginPort = 3389
		}
	}
	if rootPasswd == "" {
		rootPasswd, err = generateRandomPassword(16)
		if err != nil {
			logger.Ctx(ctx).Error("Failed to generate random password", err)
			return nil, NewCLError(ErrEncryptionFailed, "Failed to generate random password", err)
		}
		logger.Ctx(ctx).Debug("Generated random password for instance")
	}
	passwdLogin := true

	if bootPool == nil {
		if bootPool, err = BuiltinPool(ctx); err != nil {
			return
		}
	}
	if bootPool.Status != model.StoragePoolActive {
		return nil, NewCLError(ErrStoragePoolUnavailable, fmt.Sprintf("Storage pool %s is disabled", bootPool.Name), nil)
	}
	// Boot disks in the built-in pool: cland picks among the hosts whose built-in pool is not too full.
	// A host given by the admin is checked against the pool like any other allocation (§6).
	hyperGroup := ""
	if bootPool.Builtin {
		if hyperID >= 0 {
			if _, err = admitLocked(db, bootPool, int32(hyperID), int64(disk)*int64(count)); err != nil {
				return
			}
		} else if hyperGroup, err = instanceHyperGroup(db, zoneID); err != nil {
			logger.Ctx(ctx).Error("No valid hypervisor", err)
			return
		}
	}

	// 镜像下载地址对本次创建的所有实例相同：在建实例/卷/网卡之前生成，S3 不可用时直接失败，避免白做一轮再回滚
	imageDownloadURLB64, err := BuildImageDownloadURLParam(ctx, image)
	if err != nil {
		return nil, err
	}
	execCommands = []*ExecutionCommand{}
	i := 0
	hostname := prefix
	for i < count {
		if count > 1 {
			hostname = fmt.Sprintf("%s-%d", prefix, i+1)
		}
		var total int64
		if err = db.Unscoped().Model(&model.Instance{}).Where("image_id = ?", image.ID).Count(&total).Error; err != nil {
			logger.Ctx(ctx).Error("Failed to query total instances with the image", err)
			return nil, NewCLError(ErrSQLSyntaxError, "Failed to query total instances with the image", err)
		}
		snapshot := total/MaxmumSnapshot + 1 // Same snapshot reference can not be over 128, so use 96 here
		instance := &model.Instance{
			Model:          model.Model{Creater: memberShip.UserID},
			Owner:          memberShip.OrgID,
			Hostname:       hostname,
			ImageID:        image.ID,
			Snapshot:       int64(snapshot),
			Keys:           keys,
			RootPasswd:     rootPasswd,
			PasswdLogin:    passwdLogin,
			LoginPort:      int32(loginPort),
			Userdata:       userdata,
			UserdataType:   userdataType,
			Vendordata:     vendorData,
			VendordataType: vendorDataType,
			Status:         model.InstanceStatusProvisioning,
			ZoneID:         zoneID,
			RouterID:       routerID,
			Cpu:            cpu,
			Memory:         memory,
			Disk:           disk,
		}
		err = db.Create(instance).Error
		if err != nil {
			logger.Ctx(ctx).Error("DB create instance failed", err)
			return nil, NewCLError(ErrInstanceCreationFailed, "Failed to create instance record", err)
		}
		instance.Image = image
		instance.Zone = zone
		var bootVolume *model.Volume
		imagePrefix := fmt.Sprintf("image-%d-%s", image.ID, strings.Split(image.UUID, "-")[0])
		// boot volume name format: instance-15-boot-volume-10
		bootVolume, err = volumeAdmin.CreateVolume(ctx, fmt.Sprintf("instance-%d-boot-volume", instance.ID), instance.Disk, instance.ID, true, bootPool)
		if err != nil {
			logger.Ctx(ctx).Error("Failed to create boot volume", err)
			return
		}
		metadata := ""
		var ifaces []*model.Interface
		// cloud-init does not support set encrypted password for windows
		// so we only encrypt the password for linux and others
		instancePasswd := rootPasswd
		if rootPasswd != "" && image.OSCode != "windows" {
			instancePasswd, err = encrpt.Mkpasswd(rootPasswd, "sha512")
			if err != nil {
				logger.Ctx(ctx).Errorf("Failed to encrypt admin password, %v", err)
				return nil, NewCLError(ErrEncryptionFailed, "Failed to encrypt admin password", err)
			}
		}
		err = (&InterfaceAdminService{}).CheckIfaceSubnets(ctx, primaryIface, secondaryIfaces)
		if err != nil {
			logger.Ctx(ctx).Error("Invalid or duplicate subnets for interfaces", err)
			return nil, NewCLError(ErrInterfaceInvalidSubnet, "Invalid or duplicate subnets for interfaces", err)
		}

		ifaces, metadata, err = a.buildMetadata(ctx, primaryIface, secondaryIfaces, instancePasswd, loginPort, keys, instance, "")
		if err != nil {
			logger.Ctx(ctx).Error("Build instance metadata failed", err)
			return nil, NewCLError(ErrInvalidMetadata, "Failed to build instance metadata", err)
		}
		instance.Interfaces = ifaces
		builtinGB := int64(0)
		if bootPool.Builtin {
			builtinGB = int64(instance.Disk)
		}
		rcNeeded := schedulerResources(instance.Cpu, instance.Memory, builtinGB)
		control := "select=" + hyperGroup + " " + rcNeeded
		if !bootPool.Builtin {
			// Boot disk in a local pool (L4): clapi picks the host and holds the room until the disk is created
			var host int32
			if host, err = bootHost(db, bootPool, zoneID, hyperID, instance.Disk, instance.Cpu, instance.Memory); err != nil {
				return
			}
			if _, err = reserve(db, host, bootPool.ID, bootVolume.ID, 0, model.ReservationBoot, instance.Disk, reservationTTL); err != nil {
				return nil, NewCLError(ErrSQLSyntaxError, "Failed to reserve storage for the boot disk", err)
			}
			control = fmt.Sprintf("select=%s %s", hyperGroupOf(zoneID, []int32{host}), rcNeeded)
		} else if hyperID >= 0 {
			// Every instance of the batch goes to the given host, not only the first one. cland checks nothing for
			// inter=, and the boot volume has no host until the node created the disk, so the room is held meanwhile
			// like it is for the other pools
			if _, err = reserve(db, int32(hyperID), bootPool.ID, bootVolume.ID, 0, model.ReservationBoot, instance.Disk, reservationTTL); err != nil {
				return nil, NewCLError(ErrSQLSyntaxError, "Failed to reserve storage for the boot disk", err)
			}
			control = fmt.Sprintf("inter=%d %s", hyperID, rcNeeded)
		}
		command := fmt.Sprintf("/opt/cloudland/scripts/backend/launch_vm.sh '%d' '%s.%s' '%t' '%d' '%s' '%d' '%d' '%d' '%d' '%t' '%s' '%s' '%s' '%s' '%s'<<'EOF'\n%s\nEOF", instance.ID, ShellEscape(imagePrefix), ShellEscape(image.Format), image.QAEnabled, snapshot, ShellEscape(hostname), instance.Cpu, instance.Memory, instance.Disk, bootVolume.ID, nestedEnable, ShellEscape(image.BootLoader), ShellEscape(instance.UUID), ShellEscape(imageDownloadURLB64), ShellEscape(PoolScriptID(bootPool)), ShellEscape(bootVolume.Path), base64.StdEncoding.EncodeToString([]byte(metadata)))
		execCommands = append(execCommands, &ExecutionCommand{
			Control: control,
			Command: command,
		})
		instances = append(instances, instance)
		i++
	}
	return
}

func (a *InstanceAdmin) Rescue(ctx context.Context, instance *model.Instance, rescueImage *model.Image, rootPasswd string) (err error) {
	logger.Ctx(ctx).Infof("ENTER InstanceAdmin.Rescue: instanceID=%d, rescueImageID=%v", instance.ID, rescueImage)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT InstanceAdmin.Rescue: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT InstanceAdmin.Rescue: success")
		}
	}()
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	if instance.Status == "rescuing" {
		err = NewCLError(ErrInstanceInvalidState, "Instance is already in rescue status", nil)
		return
	}
	image := instance.Image
	if rescueImage == nil {
		if image.RescueImage <= 0 {
			err = NewCLError(ErrRescueImageNotFound, "No rescue image specified for the instance image", nil)
			return
		}
		rescueImage, err = (&ImageAdminService{}).Get(ctx, image.RescueImage)
		if err != nil {
			return
		}
	}
	err = db.Model(instance).Update("status", "rescuing").Error
	if err != nil {
		logger.Ctx(ctx).Error("Update instance status to rescuing failed", err)
		return NewCLError(ErrInstanceUpdateFailed, "Failed to update instance status to rescuing", err)
	}
	logger.Ctx(ctx).Debugf("Rescue image is %s", rescueImage.Name)
	imagePrefix := fmt.Sprintf("image-%d-%s", rescueImage.ID, strings.Split(rescueImage.UUID, "-")[0])
	bootVolume := &model.Volume{}
	if err = db.Where("instance_id = ? and booting = true", instance.ID).Take(bootVolume).Error; err != nil {
		logger.Ctx(ctx).Error("Failed to query boot volume, %v", err)
		return NewCLError(ErrBootVolumeNotFound, "Failed to query boot volume", err)
	}
	bootPool, err := VolumePool(ctx, bootVolume)
	if err != nil {
		return
	}
	if _, err = poolUsableOn(db, bootPool, instance.Hyper, false); err != nil {
		return
	}
	metadata := ""
	metadata, err = a.GetMetadata(ctx, instance, rootPasswd)
	if err != nil {
		logger.Ctx(ctx).Error("Build instance metadata failed", err)
		return
	}
	control := fmt.Sprintf("inter=%d", instance.Hyper)
	var imageDownloadURLB64 string
	imageDownloadURLB64, err = BuildImageDownloadURLParam(ctx, rescueImage)
	if err != nil {
		return
	}
	command := fmt.Sprintf("/opt/cloudland/scripts/backend/rescue_vm.sh '%d' '%s.%s' '%s' '%d' '%d' '%d' '%d' '%s' '%s' '%s' '%s' <<'EOF'\n%s\nEOF", instance.ID, ShellEscape(imagePrefix), ShellEscape(rescueImage.Format), ShellEscape(instance.Hostname), instance.Cpu, instance.Memory, instance.Disk, bootVolume.ID, ShellEscape(rescueImage.BootLoader), ShellEscape(instance.UUID), ShellEscape(imageDownloadURLB64), ShellEscape(VolumeAbsPath(bootPool, bootVolume)), base64.StdEncoding.EncodeToString([]byte(metadata)))
	err = HyperExecute(ctx, control, command)
	if err != nil {
		logger.Ctx(ctx).Error("Delete vm command execution failed", err)
		return
	}
	return
}

func (a *InstanceAdmin) EndRescue(ctx context.Context, instance *model.Instance) (err error) {
	logger.Ctx(ctx).Infof("ENTER InstanceAdmin.EndRescue: instanceID=%d", instance.ID)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT InstanceAdmin.EndRescue: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT InstanceAdmin.EndRescue: success")
		}
	}()
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	if instance.Status != "rescuing" {
		err = NewCLError(ErrInstanceInvalidState, "Instance is not in rescue status", nil)
		return
	}
	err = db.Model(instance).Update("status", "shut_off").Error
	if err != nil {
		logger.Ctx(ctx).Error("Update instance status to rescuing failed", err)
		return NewCLError(ErrInstanceUpdateFailed, "Failed to update instance status to rescuing", err)
	}
	control := fmt.Sprintf("inter=%d", instance.Hyper)
	command := fmt.Sprintf("/opt/cloudland/scripts/backend/end_rescue.sh '%d'", instance.ID)
	err = HyperExecute(ctx, control, command)
	if err != nil {
		logger.Ctx(ctx).Error("Delete vm command execution failed", err)
		return
	}
	return
}

func (a *InstanceAdmin) executeCommandList(ctx context.Context, cmdList []*ExecutionCommand) {
	logger.Ctx(ctx).Infof("ENTER InstanceAdmin.executeCommandList: cmdCount=%d", len(cmdList))
	defer logger.Ctx(ctx).Info("EXIT InstanceAdmin.executeCommandList")
	var err error
	for _, cmd := range cmdList {
		err = HyperExecute(ctx, cmd.Control, cmd.Command)
		if err != nil {
			logger.Ctx(ctx).Error("Command execution failed", err)
		}
	}
}

func (a *InstanceAdmin) ChangeInstanceStatus(ctx context.Context, instance *model.Instance, action string) (err error) {
	logger.Ctx(ctx).Infof("ENTER InstanceAdmin.ChangeInstanceStatus: instanceID=%d, action=%s", instance.ID, action)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT InstanceAdmin.ChangeInstanceStatus: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT InstanceAdmin.ChangeInstanceStatus: success")
		}
	}()
	control := fmt.Sprintf("inter=%d", instance.Hyper)
	command := fmt.Sprintf("/opt/cloudland/scripts/backend/action_vm.sh '%d' '%s'", instance.ID, ShellEscape(action))
	err = HyperExecute(ctx, control, command)
	if err != nil {
		logger.Ctx(ctx).Error("Delete vm command execution failed", err)
		return
	}
	return
}

func (a *InstanceAdmin) Update(ctx context.Context, instance *model.Instance, hostname string, action PowerAction, hyperID int) (err error) {
	logger.Ctx(ctx).Infof("ENTER InstanceAdmin.Update: instanceID=%d, hostname=%s, action=%s, hyperID=%d", instance.ID, hostname, action, hyperID)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT InstanceAdmin.Update: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT InstanceAdmin.Update: success")
		}
	}()
	if instance.Status == model.InstanceStatusMigrating {
		err = fmt.Errorf("Instance is not in a valid state")
		return
	}
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckResourceOrg(model.OrgWriter, instance.Owner)
	if !permit {
		logger.Ctx(ctx).Error("Not authorized to update the instance")
		err = NewCLError(ErrPermissionDenied, "Not authorized to update the instance", nil)
		return
	}

	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	if hyperID != int(instance.Hyper) {
		permit = memberShip.IsSystemAdmin()
		if !permit {
			logger.Ctx(ctx).Error("Not authorized to migrate VM")
			err = NewCLError(ErrPermissionDenied, "Not authorized to migrate VM", nil)
			return
		}
		// TODO: migrate VM
	}
	if instance.Hostname != hostname {
		instance.Hostname = hostname
	}
	if err = db.Model(&model.Instance{}).Where("id = ?", instance.ID).Updates(map[string]interface{}{"hostname": instance.Hostname}).Error; err != nil {
		logger.Ctx(ctx).Error("Failed to save instance", err)
		return NewCLError(ErrInstanceUpdateFailed, "Failed to save instance", err)
	}
	if string(action) != "" {
		err = instanceAdmin.ChangeInstanceStatus(ctx, instance, string(action))
		if err != nil {
			logger.Ctx(ctx).Error("action vm command execution failed", err)
			return
		}
	}
	return
}

func (a *InstanceAdmin) Resize(ctx context.Context, instance *model.Instance, cpu int32, memory int32) (err error) {
	logger.Ctx(ctx).Infof("ENTER InstanceAdmin.Resize: instanceID=%d, cpu=%d, memory=%d", instance.ID, cpu, memory)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT InstanceAdmin.Resize: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT InstanceAdmin.Resize: success")
		}
	}()
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckResourceOrg(model.OrgWriter, instance.Owner)
	if !permit {
		logger.Ctx(ctx).Error("Not authorized to reinstall the instance")
		err = fmt.Errorf("Not authorized")
		return
	}
	var bootVolume *model.Volume
	for _, volume := range instance.Volumes {
		if volume.Booting {
			bootVolume = volume
			break
		}
	}
	if bootVolume == nil {
		logger.Ctx(ctx).Error("Instance has no boot volume")
		err = NewCLError(ErrBootVolumeNotFound, "Instance has no boot volume", nil)
		return
	}
	status := model.InstanceStatusResizing
	if instance.Status == status {
		logger.Ctx(ctx).Error("Instance is already resizing")
		err = NewCLError(ErrInstanceInvalidState, "Instance is already resizing", nil)
		return
	}
	instance.Status = status
	instance.Cpu = cpu
	instance.Memory = memory
	if instance.Disk == 0 {
		instance.Disk = bootVolume.Size
	}
	if err = db.Save(&instance).Error; err != nil {
		logger.Ctx(ctx).Error("Failed to save instance", err)
		return NewCLError(ErrInstanceUpdateFailed, "Failed to save instance", err)
	}
	err = db.Model(&model.Instance{}).Where("id = ?", instance.ID).Updates(map[string]interface{}{
		"flavor_id": 0,
	}).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to save instance", err)
		return NewCLError(ErrInstanceUpdateFailed, "Failed to save instance", err)
	}

	control := fmt.Sprintf("inter=%d", instance.Hyper)
	command := fmt.Sprintf("/opt/cloudland/scripts/backend/resize_vm.sh '%d' '%d' '%d'", instance.ID, cpu, memory)
	err = HyperExecute(ctx, control, command)
	if err != nil {
		logger.Ctx(ctx).Error("Resize remote exec failed", err)
		return
	}
	return
}

func (a *InstanceAdmin) Reinstall(ctx context.Context, instance *model.Instance, image *model.Image, rootPasswd string, keys []*model.Key, cpu int32, memory int32, disk int32, loginPort int) (err error) {
	logger.Ctx(ctx).Infof("ENTER InstanceAdmin.Reinstall: instanceID=%d, imageID=%d, cpu=%d, memory=%d, disk=%d", instance.ID, image.ID, cpu, memory, disk)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT InstanceAdmin.Reinstall: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT InstanceAdmin.Reinstall: success")
		}
	}()
	if instance.Status == "rescuing" {
		err = NewCLError(ErrInstanceInvalidState, "Instance is not in the right state", nil)
		logger.Ctx(ctx).Error("Instance is not in the right state")
		return
	}
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckResourceOrg(model.OrgWriter, instance.Owner)
	if !permit {
		logger.Ctx(ctx).Error("Not authorized to reinstall the instance")
		err = NewCLError(ErrPermissionDenied, "Not authorized to reinstall the instance", nil)
		return
	}
	var bootVolume *model.Volume
	for _, volume := range instance.Volumes {
		if volume.Booting {
			bootVolume = volume
			break
		}
	}
	if bootVolume == nil {
		logger.Ctx(ctx).Error("Instance has no boot volume")
		err = NewCLError(ErrBootVolumeNotFound, "Instance has no boot volume", nil)
		return
	}
	imagePrefix := fmt.Sprintf("image-%d-%s", image.ID, strings.Split(image.UUID, "-")[0])
	var total int64
	if err = db.Unscoped().Model(&model.Instance{}).Where("image_id = ?", image.ID).Count(&total).Error; err != nil {
		logger.Ctx(ctx).Error("Failed to query total instances with the image", err)
		return NewCLError(ErrInstanceNotFound, "Failed to query total instances with the image", err)
	}
	bootPool, err := VolumePool(ctx, bootVolume)
	if err != nil {
		return
	}
	if disk > bootVolume.Size {
		if _, err = admitLocked(db, bootPool, instance.Hyper, int64(disk-bootVolume.Size)); err != nil {
			return
		}
	} else if _, err = poolUsableOn(db, bootPool, instance.Hyper, false); err != nil {
		return
	}
	if image.Size > int64(disk)*1024*1024*1024 {
		err = NewCLError(ErrDiskTooSmall, "Flavor disk size is not enough for the image", nil)
		logger.Ctx(ctx).Error(err)
		return
	}

	// change vm status to reinstalling
	if loginPort <= 0 {
		switch instance.LoginPort {
		case 22, 3389:
			if image.OSCode == "windows" {
				loginPort = 3389
			} else {
				loginPort = 22
			}
		default:
			loginPort = int(instance.LoginPort)
		}
	}
	// 登录端口只写入实例元数据（由 cloud-init / guest agent 修改 sshd、RDP 端口），
	// 不再自动往安全组加 0.0.0.0/0 放行规则：安全组为多实例共享，由用户自行放行
	logger.Ctx(ctx).Debugf("Login Port is: %d", loginPort)

	if rootPasswd == "" {
		rootPasswd, err = generateRandomPassword(16)
		if err != nil {
			logger.Ctx(ctx).Error("Failed to generate random password", err)
			return NewCLError(ErrEncryptionFailed, "Failed to generate random password", err)
		}
		logger.Ctx(ctx).Debug("Generated random password for reinstall")
	}
	instance.Status = model.InstanceStatusReinstalling
	instance.LoginPort = int32(loginPort)
	instance.RootPasswd = rootPasswd
	instance.PasswdLogin = true
	instance.ImageID = image.ID
	instance.Image = image
	instance.Cpu = cpu
	instance.Memory = memory
	instance.Disk = disk
	instance.Keys = keys
	if err = db.Save(&instance).Error; err != nil {
		logger.Ctx(ctx).Error("Failed to save instance", err)
		return NewCLError(ErrInstanceUpdateFailed, "Failed to save instance", err)
	}
	err = db.Model(&model.Instance{}).Where("id = ?", instance.ID).Updates(map[string]interface{}{
		"flavor_id": 0,
	}).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to save instance", err)
		return NewCLError(ErrInstanceUpdateFailed, "Failed to save instance", err)
	}
	if err = db.Model(&instance).Association("Keys").Replace(keys); err != nil {
		logger.Ctx(ctx).Errorf("Failed to update keys association: %v", err)
		return NewCLError(ErrInstanceUpdateFailed, "Failed to update keys association", err)
	}

	// change volume status to reinstalling
	bootVolume.Status = "reinstalling"
	bootVolume.Size = disk
	if err = db.Save(&bootVolume).Error; err != nil {
		logger.Ctx(ctx).Error("Failed to save volume", err)
		return NewCLError(ErrBootVolumeUpdateFailed, "Failed to save volume", err)
	}

	// rebuild metadata
	instancePasswd := rootPasswd
	if rootPasswd != "" && image.OSCode != "windows" {
		instancePasswd, err = encrpt.Mkpasswd(rootPasswd, "sha512")
		if err != nil {
			logger.Ctx(ctx).Errorf("Failed to encrypt admin password, %v", err)
			return NewCLError(ErrEncryptionFailed, "Failed to encrypt admin password", err)
		}
	}
	metadata, err := a.GetMetadata(ctx, instance, instancePasswd)
	if err != nil {
		logger.Ctx(ctx).Error("Failed to get instance metadata", err)
		return
	}

	snapshot := total/MaxmumSnapshot + 1 // Same snapshot reference can not be over 128, so use 96 here
	control := fmt.Sprintf("inter=%d", instance.Hyper)
	var imageDownloadURLB64 string
	imageDownloadURLB64, err = BuildImageDownloadURLParam(ctx, image)
	if err != nil {
		return
	}
	command := fmt.Sprintf("/opt/cloudland/scripts/backend/reinstall_vm.sh '%d' '%s.%s' '%d' '%d' '%d' '%d' '%d' '%s' '%s' '%s' '%s' '%s' '%s'<<'EOF'\n%s\nEOF", instance.ID, ShellEscape(imagePrefix), ShellEscape(image.Format), snapshot, bootVolume.ID, cpu, memory, disk, ShellEscape(instance.Hostname), ShellEscape(image.BootLoader), ShellEscape(instance.UUID), ShellEscape(imageDownloadURLB64), ShellEscape(PoolScriptID(bootPool)), ShellEscape(bootVolume.Path), base64.StdEncoding.EncodeToString([]byte(metadata)))
	err = HyperExecute(ctx, control, command)
	if err != nil {
		logger.Ctx(ctx).Error("Reinstall remote exec failed", err)
		return
	}
	return
}

func (a *InstanceAdmin) SetUserPassword(ctx context.Context, id int64, user, password string) (err error) {
	logger.Ctx(ctx).Infof("ENTER InstanceAdmin.SetUserPassword: id=%d, user=%s", id, user)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT InstanceAdmin.SetUserPassword: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT InstanceAdmin.SetUserPassword: success")
		}
	}()
	ctx, db := GetContextDB(ctx)
	instance := &model.Instance{Model: model.Model{ID: id}}
	if err = db.Preload("Image").Take(instance).Error; err != nil {
		logger.Ctx(ctx).Error("Failed to get instance ", err)
		return NewCLError(ErrInstanceNotFound, "Failed to get instance", err)
	}
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckResourceOrg(model.OrgWriter, instance.Owner)
	if !permit {
		logger.Ctx(ctx).Error("Not authorized to set password for the instance")
		err = NewCLError(ErrPermissionDenied, "Not authorized to set password for the instance", nil)
		return
	}
	if !instance.Image.QAEnabled {
		err = NewCLError(ErrImageNoQA, "Guest Agent is not enabled for the image of instance", nil)
		logger.Ctx(ctx).Error(err)
		return
	}
	if instance.Status != model.InstanceStatusRunning {
		err = NewCLError(ErrInstanceInvalidState, "Instance is not in running state", nil)
		logger.Ctx(ctx).Error(err)
		return
	}
	control := fmt.Sprintf("inter=%d", instance.Hyper)
	command := fmt.Sprintf("/opt/cloudland/scripts/backend/set_user_passwd.sh '%d' '%s' '%s'", instance.ID, ShellEscape(user), ShellEscape(password))
	err = HyperExecute(ctx, control, command)
	if err != nil {
		logger.Ctx(ctx).Error("Set password command execution failed", err)
		return
	}
	if user == "root" {
		if err = db.Model(instance).Update("root_passwd", password).Error; err != nil {
			logger.Ctx(ctx).Error("Failed to update root password in database", err)
			return
		}
	}
	return
}

func (a *InstanceAdmin) deleteInterfaces(ctx context.Context, instance *model.Instance) (err error) {
	logger.Ctx(ctx).Infof("ENTER InstanceAdmin.deleteInterfaces: instanceID=%d", instance.ID)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT InstanceAdmin.deleteInterfaces: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT InstanceAdmin.deleteInterfaces: success")
		}
	}()
	ctx, db := GetContextDB(ctx)
	for _, iface := range instance.Interfaces {
		err = a.deleteInterface(ctx, iface)
		if err != nil {
			logger.Ctx(ctx).Error("Failed to delete interface", err)
			err = nil
			return
		}
		err = db.Model(&model.Subnet{}).Where("interface = ?", iface.ID).Updates(map[string]interface{}{
			"interface": 0}).Error
		if err != nil {
			logger.Ctx(ctx).Error("Failed to update subnet", err)
			return NewCLError(ErrSubnetUpdateFailed, "Failed to update subnet", err)
		}
	}
	return
}

func (a *InstanceAdmin) deleteInterface(ctx context.Context, iface *model.Interface) (err error) {
	logger.Ctx(ctx).Infof("ENTER InstanceAdmin.deleteInterface: ifaceID=%d", iface.ID)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT InstanceAdmin.deleteInterface: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT InstanceAdmin.deleteInterface: success")
		}
	}()
	err = DeleteInterface(ctx, iface)
	if err != nil {
		logger.Ctx(ctx).Error("Failed to create interface")
		return
	}
	vlan := iface.Address.Subnet.Vlan
	control := ""
	command := fmt.Sprintf("/opt/cloudland/scripts/backend/del_host.sh '%d' '%s' '%s'", vlan, ShellEscape(iface.MacAddr), ShellEscape(iface.Address.Address))
	err = HyperExecute(ctx, control, command)
	if err != nil {
		logger.Ctx(ctx).Error("Delete interface failed")
		return
	}
	return
}

func (a *InstanceAdmin) createInterface(ctx context.Context, ifaceInfo *InterfaceInfo, instance *model.Instance, ifname string) (iface *model.Interface, ifaceSubnet *model.Subnet, err error) {
	logger.Ctx(ctx).Infof("ENTER InstanceAdmin.createInterface: instanceID=%d, ifname=%s", instance.ID, ifname)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT InstanceAdmin.createInterface: error=%v", err)
		} else {
			logger.Ctx(ctx).Infof("EXIT InstanceAdmin.createInterface: ifaceID=%d", iface.ID)
		}
	}()
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)

	if len(ifaceInfo.PublicIps) > 0 {
		iface, ifaceSubnet, err = DerivePublicInterface(ctx, instance, nil, ifaceInfo.PublicIps, "", "")
		if err != nil {
			logger.Ctx(ctx).Error("Failed to derive primary interface", err)
			return
		}
		if len(ifaceInfo.SecurityGroups) > 0 {
			if err = db.Model(iface).Association("SecurityGroups").Replace(ifaceInfo.SecurityGroups); err != nil {
				logger.Ctx(ctx).Debug("Failed to save interface", err)
				return nil, nil, NewCLError(ErrAssociateSG2InterfaceFailed, "Failed to associate security groups with interface", err)
			}
			iface.SecurityGroups = ifaceInfo.SecurityGroups
		}
		iface.Inbound = ifaceInfo.Inbound
		iface.Outbound = ifaceInfo.Outbound
		iface.AllowSpoofing = ifaceInfo.AllowSpoofing
		err = db.Model(&model.Interface{Model: model.Model{ID: int64(iface.ID)}}).Updates(map[string]interface{}{
			"inbound":        iface.Inbound,
			"outbound":       iface.Outbound,
			"allow_spoofing": iface.AllowSpoofing,
		}).Error
		if err != nil {
			logger.Ctx(ctx).Debug("Failed to update interface", err)
			return nil, nil, NewCLError(ErrInterfaceUpdateFailed, "Failed to update interface", err)
		}
	} else {
		subnets := ifaceInfo.Subnets
		address := ifaceInfo.IpAddress
		count := ifaceInfo.Count - 1
		mac := ifaceInfo.MacAddress
		inbound := ifaceInfo.Inbound
		outbound := ifaceInfo.Outbound
		secgroups := ifaceInfo.SecurityGroups
		allowSpoofing := ifaceInfo.AllowSpoofing
		for i, subnet := range subnets {
			if subnet.Type == "site" {
				logger.Ctx(ctx).Error("Not allowed to create interface in site subnet")
				err = NewCLError(ErrNotAllowInterfaceInSiteSubnet, "Not allowed to create interface in site subnet", nil)
				return
			}
			if iface == nil {
				iface, err = CreateInterface(ctx, subnet, instance.ID, memberShip.OrgID, instance.Hyper, inbound, outbound, address, mac, ifname, "instance", secgroups, allowSpoofing)
				if err == nil {
					ifaceSubnet = subnets[i]
					if subnet.Type == "public" {
						_, err = (&FloatingIpAdminService{}).createDummyFloatingIp(ctx, instance, iface.Address.Address)
						if err != nil {
							logger.Ctx(ctx).Error("DB failed to create dummy floating ip", err)
							return
						}
					}
					break
				} else {
					logger.Ctx(ctx).Errorf("Allocate address interface from subnet %s--%s/%s failed, %v", subnet.Name, subnet.Network, subnet.Netmask, err)
				}
			}
		}
		if iface == nil {
			if err == nil {
				err = NewCLError(ErrInterfaceCreateFailed, "Failed to create interface", nil)
			}
			return
		}
		err = (&InterfaceAdminService{}).allocateSecondAddresses(ctx, instance, iface, subnets, count)
		if err != nil {
			return
		}
	}
	siteSubnets := ifaceInfo.SiteSubnets
	for _, site := range siteSubnets {
		err = db.Model(site).Updates(map[string]interface{}{"interface": iface.ID}).Error
		if err != nil {
			logger.Ctx(ctx).Error("Failed to update site subnet", err)
			return nil, nil, NewCLError(ErrSubnetUpdateFailed, "Failed to update site subnet", err)
		}
		iface.SiteSubnets = append(iface.SiteSubnets, site)
	}
	return
}

func (a *InstanceAdmin) buildMetadata(ctx context.Context, primaryIface *InterfaceInfo, secondaryIfaces []*InterfaceInfo,
	rootPasswd string, loginPort int, keys []*model.Key, instance *model.Instance, service string) (interfaces []*model.Interface, metadata string, err error) {
	logger.Ctx(ctx).Infof("ENTER InstanceAdmin.buildMetadata: instanceID=%d, loginPort=%d, service=%s", instance.ID, loginPort, service)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT InstanceAdmin.buildMetadata: error=%v", err)
		} else {
			logger.Ctx(ctx).Infof("EXIT InstanceAdmin.buildMetadata: ifaceCount=%d", len(interfaces))
		}
	}()
	vlans := []*VlanInfo{}
	instNetworks := []*InstanceNetwork{}
	instLinks := []*NetworkLink{}
	primaryIP := primaryIface.IpAddress
	inbound := primaryIface.Inbound
	outbound := primaryIface.Outbound

	iface, primary, err := a.createInterface(ctx, primaryIface, instance, "eth0")
	if err != nil {
		return
	}
	vlan := iface.Address.Subnet.Vlan
	interfaces = append(interfaces, iface)
	instLinks = append(instLinks, &NetworkLink{MacAddr: iface.MacAddr, Mtu: uint(iface.Mtu), ID: iface.Name, Type: "phy"})
	securityData, err := GetSecurityData(ctx, iface.SecurityGroups)
	if err != nil {
		logger.Ctx(ctx).Error("Get security data for interface failed", err)
		return
	}
	vlans = append(vlans, &VlanInfo{
		Device:        "eth0",
		IsPrivate:     iface.Address.Subnet.Type == "private",
		Vlan:          vlan,
		Inbound:       inbound,
		Outbound:      outbound,
		AllowSpoofing: iface.AllowSpoofing,
		Gateway:       primary.Gateway,
		Router:        primary.RouterID,
		IpAddr:        iface.Address.Address,
		MacAddr:       iface.MacAddr,
		SecRules:      securityData,
	})
	for i, ifaceInfo := range secondaryIfaces {
		var subnet *model.Subnet
		ifname := fmt.Sprintf("eth%d", i+1)
		inbound = ifaceInfo.Inbound
		outbound = ifaceInfo.Outbound
		iface, subnet, err = a.createInterface(ctx, ifaceInfo, instance, ifname)
		if err != nil {
			logger.Ctx(ctx).Errorf("Allocate address for secondary subnet %s--%s/%s failed, %v", subnet.Name, subnet.Network, subnet.Netmask, err)
			return
		}
		interfaces = append(interfaces, iface)
		instLinks = append(instLinks, &NetworkLink{MacAddr: iface.MacAddr, Mtu: uint(iface.Mtu), ID: iface.Name, Type: "phy"})
		securityData, err = GetSecurityData(ctx, iface.SecurityGroups)
		if err != nil {
			logger.Ctx(ctx).Error("Get security data for interface failed", err)
			return
		}
		vlans = append(vlans, &VlanInfo{
			Device:        ifname,
			IsPrivate:     subnet.Type == "private",
			Vlan:          subnet.Vlan,
			Inbound:       inbound,
			Outbound:      outbound,
			AllowSpoofing: iface.AllowSpoofing,
			Gateway:       subnet.Gateway,
			Router:        subnet.RouterID,
			IpAddr:        iface.Address.Address,
			MacAddr:       iface.MacAddr,
			SecRules:      securityData,
		})
	}
	var moreAddresses []string
	instNetworks, moreAddresses, err = GetInstanceNetworks(ctx, instance, interfaces)
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to get instance networks, %v", err)
		return
	}
	vlans[0].MoreAddresses = moreAddresses
	var instKeys []string
	for _, key := range keys {
		instKeys = append(instKeys, key.PublicKey)
	}
	dns := primary.NameServer
	if dns == primaryIP {
		dns = ""
	}
	instData := &InstanceData{
		Userdata:       instance.Userdata,
		UserdataType:   instance.UserdataType,
		Vendordata:     instance.Vendordata,
		VendordataType: instance.VendordataType,
		DNS:            dns,
		Vlans:          vlans,
		Networks:       instNetworks,
		Links:          instLinks,
		Keys:           instKeys,
		RootPasswd:     rootPasswd,
		LoginPort:      loginPort,
		OSCode:         GetImageOSCode(ctx, instance),
	}
	jsonData, err := json.Marshal(instData)
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to marshal instance json data, %v", err)
		return nil, "", NewCLError(ErrJSONMarshalFailed, "Failed to marshal instance json data", err)
	}
	return interfaces, string(jsonData), nil
}

func (a *InstanceAdmin) GetMetadata(ctx context.Context, instance *model.Instance, rootPasswd string) (metadata string, err error) {
	logger.Ctx(ctx).Infof("ENTER InstanceAdmin.GetMetadata: instanceID=%d", instance.ID)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT InstanceAdmin.GetMetadata: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT InstanceAdmin.GetMetadata: success")
		}
	}()
	vlans := []*VlanInfo{}
	instLinks := []*NetworkLink{}
	volumes := []*VolumeInfo{}
	instNetworks := []*InstanceNetwork{}
	var moreAddresses []string
	var instKeys []string
	for _, key := range instance.Keys {
		instKeys = append(instKeys, key.PublicKey)
	}
	for _, volume := range instance.Volumes {
		volumes = append(volumes, &VolumeInfo{
			ID:      volume.ID,
			UUID:    volume.UUID,
			Device:  volume.Target,
			Booting: volume.Booting,
		})
	}
	dns := ""
	instNetworks, moreAddresses, err = GetInstanceNetworks(ctx, instance, nil)
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to get instance networks, %v", err)
		return
	}
	for _, iface := range instance.Interfaces {
		subnet := iface.Address.Subnet
		if iface.PrimaryIf {
			dns = subnet.NameServer
		}
		instLinks = append(instLinks, &NetworkLink{MacAddr: iface.MacAddr, Mtu: uint(iface.Mtu), ID: iface.Name, Type: "phy"})
		// 迁移时目标节点按这份 vlans 预建网卡与安全组（target_migration.sh -> sync_nic_info.sh），
		// 缺少 SecRules 时只建出拒绝一切新连接的骨架规则，切换后网络要等 completed 重建安全组才通
		_, sgDB := GetContextDB(ctx)
		if err = sgDB.Model(iface).Association("SecurityGroups").Find(&iface.SecurityGroups); err != nil {
			logger.Ctx(ctx).Error("Get security groups for interface failed", err)
			return
		}
		var securityData []*SecurityData
		securityData, err = GetSecurityData(ctx, iface.SecurityGroups)
		if err != nil {
			logger.Ctx(ctx).Error("Get security data for interface failed", err)
			return
		}
		vlans = append(vlans, &VlanInfo{
			Device:        iface.Name,
			IsPrivate:     subnet.Type == "private",
			Vlan:          subnet.Vlan,
			Inbound:       iface.Inbound,
			Outbound:      iface.Outbound,
			AllowSpoofing: iface.AllowSpoofing,
			Gateway:       subnet.Gateway,
			Router:        subnet.RouterID,
			IpAddr:        iface.Address.Address,
			MacAddr:       iface.MacAddr,
			SecRules:      securityData,
			MoreAddresses: moreAddresses,
		})
	}
	instData := &InstanceData{
		Userdata:       instance.Userdata,
		UserdataType:   instance.UserdataType,
		Vendordata:     instance.Vendordata,
		VendordataType: instance.VendordataType,
		DNS:            dns,
		Vlans:          vlans,
		Networks:       instNetworks,
		Links:          instLinks,
		Volumes:        volumes,
		Keys:           instKeys,
		RootPasswd:     rootPasswd,
		LoginPort:      int(instance.LoginPort),
		OSCode:         GetImageOSCode(ctx, instance),
	}
	jsonData, err := json.Marshal(instData)
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to marshal instance json data, %v", err)
		return "", NewCLError(ErrJSONMarshalFailed, "Failed to marshal instance json data", err)
	}
	return string(jsonData), nil
}

// CleanupInstanceRuleLinks cleans up all rule links for an instance
// This ensures data consistency when deleting an instance
func (a *InstanceAdmin) CleanupInstanceRuleLinks(ctx context.Context, instanceUUID string) (err error) {
	logger.Ctx(ctx).Infof("ENTER InstanceAdmin.CleanupInstanceRuleLinks: instanceUUID=%s", instanceUUID)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT InstanceAdmin.CleanupInstanceRuleLinks: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT InstanceAdmin.CleanupInstanceRuleLinks: success")
		}
	}()
	alarmOp := &AlarmOperator{}
	linksMap, err := alarmOp.GetInstanceRuleLinks(ctx, []string{instanceUUID})
	if err != nil {
		return fmt.Errorf("failed to get rule links: %w", err)
	}

	links := linksMap[instanceUUID]
	if len(links) == 0 {
		logger.Ctx(ctx).Infof("No rule links for instance %s", instanceUUID)
		return nil
	}

	logger.Ctx(ctx).Infof("Cleaning up %d rule links for instance %s", len(links), instanceUUID)

	// Group by (group_uuid, rule_type) for batch cleanup
	type groupKey struct {
		GroupUUID string
		RuleType  string
	}
	groupedLinks := make(map[groupKey][]string) // map[key][]interfaces

	ctx, db := GetContextDB(ctx)
	for _, link := range links {
		// Determine rule type
		var group model.RuleGroupV2
		ruleType := "alarm-cpu" // default
		if err := db.Where("uuid = ?", link.GroupUUID).First(&group).Error; err == nil {
			category := "alarm"
			if group.Type == model.RuleTypeAdjustCPU || group.Type == model.RuleTypeAdjustInBW || group.Type == model.RuleTypeAdjustOutBW {
				category = "adjust"
			}
			subType := "cpu"
			if strings.Contains(group.Type, "bw") {
				subType = "bw"
			}
			ruleType = fmt.Sprintf("%s-%s", category, subType)
		}

		key := groupKey{link.GroupUUID, ruleType}
		groupedLinks[key] = append(groupedLinks[key], link.Interface)

		// Delete from DB
		if _, err := alarmOp.DeleteVMLink(ctx, link.GroupUUID, instanceUUID, link.Interface); err != nil {
			logger.Ctx(ctx).Error("Failed to delete link", err)
		}
	}

	// Update matched_vms.json for each group
	for key, interfaces := range groupedLinks {
		if err := UpdateMatchedVMsJSON(ctx, []string{instanceUUID}, key.GroupUUID, "remove", key.RuleType, interfaces...); err != nil {
			logger.Ctx(ctx).Error("Failed to update matched_vms.json", err)
		}
	}

	logger.Ctx(ctx).Infof("Successfully cleaned up rule links for instance %s", instanceUUID)
	return nil
}

func (a *InstanceAdmin) Delete(ctx context.Context, instance *model.Instance) (err error) {
	logger.Ctx(ctx).Infof("ENTER InstanceAdmin.Delete: instanceID=%d", instance.ID)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT InstanceAdmin.Delete: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT InstanceAdmin.Delete: success")
		}
	}()
	if instance.Status == model.InstanceStatusMigrating {
		err = NewCLError(ErrInstanceInvalidState, "Instance is not in a valid state", nil)
		return
	}
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckResourceOrg(model.OrgWriter, instance.Owner)
	if !permit {
		logger.Ctx(ctx).Error("Not authorized to delete the instance")
		err = NewCLError(ErrPermissionDenied, "Not authorized to delete the instance", nil)
		return
	}
	var moreAddresses []string
	for _, iface := range instance.Interfaces {
		if iface.PrimaryIf {
			_, moreAddresses, err = GetInstanceNetworks(ctx, instance, []*model.Interface{iface})
			if err != nil {
				logger.Ctx(ctx).Errorf("Failed to get instance networks, %v", err)
				return
			}
			for _, site := range iface.SiteSubnets {
				err = db.Model(site).Updates(map[string]interface{}{"interface": 0}).Error
				if err != nil {
					logger.Ctx(ctx).Error("Failed to update interface", err)
				}
			}
		}
	}
	if err = db.Preload("Group").Where("instance_id = ?", instance.ID).Order("updated_at").Find(&instance.FloatingIps).Error; err != nil {
		logger.Ctx(ctx).Errorf("Failed to query floating ip(s), %v", err)
		return NewCLError(ErrSQLSyntaxError, "Failed to query floating ip(s) for instance", err)
	}
	if instance.FloatingIps != nil {
		for _, fip := range instance.FloatingIps {
			fip.Instance = instance
			err = (&FloatingIpAdminService{}).Detach(ctx, fip)
			if err != nil {
				logger.Ctx(ctx).Errorf("Failed to detach floating ip, %v", err)
				return
			}
		}
		instance.FloatingIps = nil
	}
	if err = db.Where("instance_id = ?", instance.ID).Find(&instance.Volumes).Error; err != nil {
		logger.Ctx(ctx).Errorf("Failed to query volumes, %v", err)
		return NewCLError(ErrSQLSyntaxError, "Failed to query volumes for instance", err)
	}

	// The node deletes the boot disk file; none is passed when its pool was declared lost
	bootPoolID, bootPath := "-", ""
	if instance.Volumes != nil {
		for _, volume := range instance.Volumes {
			if volume.Booting {
				if volume.Status != model.VolumeStatusLost {
					if pool, perr := VolumePool(ctx, volume); perr == nil {
						bootPoolID, bootPath = PoolScriptID(pool), volume.Path
					}
				}
				// delete the boot volume directly
				if err = db.Delete(volume).Error; err != nil {
					logger.Ctx(ctx).Error("DB: delete boot volume failed", err)
					return NewCLError(ErrBootVolumeDeleteFailed, "Delete boot volume failed", err)
				}
			}
		}
		instance.Volumes = nil
	}

	// Cleanup rule links and matched_vms.json
	if cleanupErr := instanceAdmin.CleanupInstanceRuleLinks(ctx, instance.UUID); cleanupErr != nil {
		logger.Ctx(ctx).Error("Failed to cleanup rule links", cleanupErr)
	}

	control := fmt.Sprintf("inter=%d", instance.Hyper)
	if instance.Hyper == -1 {
		control = "toall="
	}
	instance.Status = model.InstanceStatusDeleting
	err = db.Model(instance).Updates(map[string]interface{}{"status": model.InstanceStatusDeleting}).Error
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to mark vm as deleting ", err)
		return NewCLError(ErrInstanceUpdateFailed, "Failed to mark vm as deleting", err)
	}
	moreAddrsJson, err := json.Marshal(moreAddresses)
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to marshal sites info, %v", err)
		return NewCLError(ErrJSONMarshalFailed, "Failed to marshal sites info", err)
	}
	command := fmt.Sprintf("/opt/cloudland/scripts/backend/clear_vm.sh '%d' '%d' '%s' '%s'<<'EOF'\n%s\nEOF", instance.ID, instance.RouterID, ShellEscape(bootPoolID), ShellEscape(bootPath), moreAddrsJson)
	err = HyperExecute(ctx, control, command)
	if err != nil {
		logger.Ctx(ctx).Error("Delete vm command execution failed ", err)
		return
	}
	return
}

func (a *InstanceAdmin) Get(ctx context.Context, id int64) (instance *model.Instance, err error) {
	logger.Ctx(ctx).Infof("ENTER InstanceAdmin.Get: id=%d", id)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT InstanceAdmin.Get: error=%v", err)
		} else {
			logger.Ctx(ctx).Infof("EXIT InstanceAdmin.Get: success, instanceUUID=%s", instance.UUID)
		}
	}()
	if id <= 0 {
		err = fmt.Errorf("Invalid instance ID: %d", id)
		logger.Ctx(ctx).Error(err)
		return
	}
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	where, args := memberShip.GetOrgFilter()
	instance = &model.Instance{Model: model.Model{ID: id}}
	if err = db.Preload("Volumes").Preload("Image").Preload("Zone").Preload("Flavor").Preload("Keys").Where(where, args...).Take(instance).Error; err != nil {
		logger.Ctx(ctx).Errorf("Failed to query instance, %v", err)
		return nil, NewCLError(ErrInstanceNotFound, "Instance not found", err)
	}

	if err = db.Preload("Group").Preload("Subnet").Where("instance_id = ?", instance.ID).Order("updated_at").Find(&instance.FloatingIps).Error; err != nil {
		logger.Ctx(ctx).Errorf("Failed to query floating ip(s), %v", err)
		return nil, NewCLError(ErrSQLSyntaxError, "Failed to query floating ip(s) for instance", err)
	}
	if err = db.Preload("SiteSubnets").Preload("SiteSubnets.Group").Preload("SecurityGroups").Preload("Address").Preload("Address.Subnet").Preload("SecondAddresses", func(db *gorm.DB) *gorm.DB {
		return db.Order("addresses.updated_at")
	}).Preload("SecondAddresses.Subnet").Where("instance = ?", instance.ID).Find(&instance.Interfaces).Error; err != nil {
		logger.Ctx(ctx).Errorf("Failed to query interfaces %v", err)
		return nil, NewCLError(ErrSQLSyntaxError, "Failed to query interfaces for instance", err)
	}
	if instance.RouterID > 0 {
		instance.Router = &model.Router{Model: model.Model{ID: instance.RouterID}}
		if err = db.Take(instance.Router).Error; err != nil {
			logger.Ctx(ctx).Errorf("Failed to query router, %v", err)
			err = NewCLError(ErrRouterNotFound, "Failed to query router for instance", err)
			return
		}
	}
	permit := memberShip.CheckResourceOrg(model.OrgReader, instance.Owner)
	if !permit {
		logger.Ctx(ctx).Error("Not authorized to read the instance")
		err = NewCLError(ErrPermissionDenied, "Not authorized to read the instance", nil)
		return
	}
	permit = memberShip.IsSystemAdmin()
	if permit {
		instance.OwnerInfo = &model.Organization{Model: model.Model{ID: instance.Owner}}
		if err = db.Take(instance.OwnerInfo).Error; err != nil {
			logger.Ctx(ctx).Error("Failed to query owner info", err)
			return nil, NewCLError(ErrOwnerNotFound, "Failed to query owner info for instance", err)
		}
	}

	return
}

func (a *InstanceAdmin) GetInstanceByUUID(ctx context.Context, uuID string) (instance *model.Instance, err error) {
	logger.Ctx(ctx).Infof("ENTER InstanceAdmin.GetInstanceByUUID: uuID=%s", uuID)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT InstanceAdmin.GetInstanceByUUID: error=%v", err)
		} else {
			logger.Ctx(ctx).Infof("EXIT InstanceAdmin.GetInstanceByUUID: success, instanceID=%d", instance.ID)
		}
	}()
	ctx, db := GetContextDB(ctx)

	instance = &model.Instance{}
	if err = db.Where("uuid = ?", uuID).Take(instance).Error; err != nil {
		logger.Ctx(ctx).Errorf("Failed to query instance, %v", err)
		return nil, NewCLError(ErrInstanceNotFound, "Instance not found", err)
	}
	return a.Get(ctx, instance.ID)
}

func GetDBIndexByInstanceUUID(c *gin.Context, uuid string) (id int, err error) {
	logger.Ctx(c).Infof("ENTER GetDBIndexByInstanceUUID: uuid=%s", uuid)
	defer func() {
		if err != nil {
			logger.Ctx(c).Errorf("EXIT GetDBIndexByInstanceUUID: error=%v", err)
		} else {
			logger.Ctx(c).Infof("EXIT GetDBIndexByInstanceUUID: id=%d", id)
		}
	}()
	db := DB()

	var instance model.Instance
	if err = db.Model(&model.Instance{}).
		Select("id").
		Where("uuid = ?", uuid).
		First(&instance).Error; err != nil {

		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Instance not found"})
			fmt.Printf("Instance not found: %s\n", uuid)
			return -1, NewCLError(ErrInstanceNotFound, "Instance not found", err)
		}
		logger.Ctx(c).Error("Database error for UUID %s: %v", uuid, err)
		return -1, NewCLError(ErrDatabaseError, "Database error", err)
	}

	return int(instance.ID), nil
}

func GetInstanceUUIDByDomain(ctx context.Context, domain string) (uuid string, err error) {
	logger.Ctx(ctx).Infof("ENTER GetInstanceUUIDByDomain: domain=%s", domain)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT GetInstanceUUIDByDomain: error=%v", err)
		} else {
			logger.Ctx(ctx).Infof("EXIT GetInstanceUUIDByDomain: uuid=%s", uuid)
		}
	}()
	// Parse domain format, example: inst-12345 -> ID=12345
	if !strings.HasPrefix(domain, "inst-") {
		logger.Ctx(ctx).Error("Invalid domain format, must start with 'inst-'")
		err = NewCLError(ErrInvalidDomainFormat, "Invalid domain format, must start with 'inst-'", nil)
		return "", err
	}

	idStr := strings.TrimPrefix(domain, "inst-")
	instanceID, err := strconv.Atoi(idStr)
	if err != nil {
		logger.Ctx(ctx).Error("Domain conversion failed domain=%s error=%v", domain, err)
		return "", NewCLError(ErrInvalidDomainFormat, "Invalid domain format, ID part is not an integer", err)
	}

	var instance model.Instance
	db := DB()
	if err = db.Where("id = ?", instanceID).First(&instance).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			logger.Ctx(ctx).Error("Instance not found domain=%s id=%d", domain, instanceID)
			return "", NewCLError(ErrInstanceNotFound, "Instance not found", err)
		}
		logger.Ctx(ctx).Error("Database query failed domain=%s error=%v", domain, err)
		return "", NewCLError(ErrDatabaseError, "Database error", err)
	}

	return instance.UUID, nil
}

func GetDomainByInstanceUUID(ctx context.Context, uuid string) (domain string, err error) {
	logger.Ctx(ctx).Infof("ENTER GetDomainByInstanceUUID: uuid=%s", uuid)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT GetDomainByInstanceUUID: error=%v", err)
		} else {
			logger.Ctx(ctx).Infof("EXIT GetDomainByInstanceUUID: domain=%s", domain)
		}
	}()
	var instance model.Instance
	db := DB()
	if err = db.Where("uuid = ?", uuid).First(&instance).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			logger.Ctx(ctx).Error("Instance not found uuid=%s", uuid)
			return "", fmt.Errorf("instance not found")
		}
		logger.Ctx(ctx).Error("Database query failed uuid=%s error=%v", uuid, err)
		return "", fmt.Errorf("database error")
	}

	// Convert instance ID to domain format: inst-{ID}
	domain = fmt.Sprintf("inst-%d", instance.ID)
	return domain, nil
}

// hyperID >= 0 时只返回该节点上的虚拟机（传 -1 不过滤）；routerID > 0 时只返回该 VPC 路由器下的虚拟机（传 0 不过滤）
func (a *InstanceAdmin) List(ctx context.Context, offset, limit int64, order, query string, hyperID int32, routerID int64) (total int64, instances []*model.Instance, err error) {
	logger.Ctx(ctx).Infof("ENTER InstanceAdmin.List: offset=%d, limit=%d, order=%s, query=%s", offset, limit, order, query)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT InstanceAdmin.List: error=%v", err)
		} else {
			logger.Ctx(ctx).Infof("EXIT InstanceAdmin.List: total=%d, count=%d", total, len(instances))
		}
	}()
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckOrgPermission(model.OrgReader) {
		logger.Ctx(ctx).Error("Not authorized for this operation")
		err = NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
		return
	}
	if limit == 0 {
		limit = 16
	}
	if order == "" {
		order = "created_at"
	}

	// Build base query with parameterized WHERE to prevent SQL injection
	where, args := memberShip.GetOrgFilter()
	baseQuery := DB().Where(where, args...)
	if query != "" {
		baseQuery = baseQuery.Where("hostname LIKE ?", "%"+query+"%")
	}
	if hyperID >= 0 {
		baseQuery = baseQuery.Where("hyper = ?", hyperID)
	}
	if routerID > 0 {
		baseQuery = baseQuery.Where("router_id = ?", routerID)
	}

	// Count total
	instances = []*model.Instance{}
	if err = baseQuery.Model(&model.Instance{}).Count(&total).Error; err != nil {
		logger.Ctx(ctx).Errorf("Failed to query total instances, %v", err)
		return 0, nil, NewCLError(ErrSQLSyntaxError, "Failed to query total instances", err)
	}

	// Fetch instances with top-level preloads (skip Interfaces here, loaded in detail below)
	listQuery := dbs.Sortby(DB().Where(where, args...).Offset(int(offset)).Limit(int(limit)), order)
	if query != "" {
		listQuery = listQuery.Where("hostname LIKE ?", "%"+query+"%")
	}
	if hyperID >= 0 {
		listQuery = listQuery.Where("hyper = ?", hyperID)
	}
	if routerID > 0 {
		listQuery = listQuery.Where("router_id = ?", routerID)
	}
	listQuery = listQuery.Preload("Volumes").Preload("Image").Preload("Zone").Preload("Flavor").Preload("Keys")
	if err = listQuery.Find(&instances).Error; err != nil {
		logger.Ctx(ctx).Errorf("Failed to query instances, %v", err)
		return 0, nil, NewCLError(ErrSQLSyntaxError, "Failed to query instances", err)
	}

	// Load sub-resources per instance using clean DB() to avoid preload pollution
	isAdmin := memberShip.IsSystemAdmin()
	for _, instance := range instances {
		if err = DB().Preload("SiteSubnets").Preload("SiteSubnets.Group").Preload("SecurityGroups").Preload("Address").Preload("Address.Subnet").Preload("SecondAddresses", func(db *gorm.DB) *gorm.DB {
			return db.Order("addresses.updated_at")
		}).Preload("SecondAddresses.Subnet").Where("instance = ?", instance.ID).Find(&instance.Interfaces).Error; err != nil {
			logger.Ctx(ctx).Errorf("Failed to query interfaces %v", err)
			err = NewCLError(ErrSQLSyntaxError, "Failed to query interfaces", err)
			return
		}

		if err = DB().Preload("Group").Preload("Subnet").Order("updated_at").Where("instance_id = ?", instance.ID).Find(&instance.FloatingIps).Error; err != nil {
			logger.Ctx(ctx).Errorf("Failed to query floating ip(s), %v", err)
			err = NewCLError(ErrSQLSyntaxError, "Failed to query floating ip(s) for instance", err)
			return
		}

		if instance.RouterID > 0 {
			instance.Router = &model.Router{Model: model.Model{ID: instance.RouterID}}
			if queryErr := DB().Take(instance.Router).Error; queryErr != nil {
				logger.Ctx(ctx).Infof("Failed to query router for instance %d, skipping: %v", instance.ID, queryErr)
				instance.Router = nil
			}
		}
		if isAdmin {
			instance.OwnerInfo = &model.Organization{Model: model.Model{ID: instance.Owner}}
			if queryErr := DB().Take(instance.OwnerInfo).Error; queryErr != nil {
				logger.Ctx(ctx).Infof("Failed to query owner info for instance %d, skipping: %v", instance.ID, queryErr)
				instance.OwnerInfo = nil
			}
		}
	}

	return
}
