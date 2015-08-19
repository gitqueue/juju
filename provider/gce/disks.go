// Copyright 2015 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package gce

import (
	"fmt"
	"strings"
	"sync"

	"github.com/juju/errors"
	"github.com/juju/utils"
	"github.com/juju/utils/set"

	"github.com/juju/juju/environs/config"
	"github.com/juju/juju/provider/gce/google"
	"github.com/juju/juju/storage"
)

const (
	GCE_ProviderType = storage.ProviderType("gce")
)

func init() {
	//TODO(perrito666) ask axw about this pool thing.
}

type gceStorageProvider struct{}

var _ storage.Provider = (*gceStorageProvider)(nil)

func (g *gceStorageProvider) ValidateConfig(cfg *storage.Config) error {
	return nil
}

func (g *gceStorageProvider) Supports(k storage.StorageKind) bool {
	return k == storage.StorageKindBlock
}

func (g *gceStorageProvider) Scope() storage.Scope {
	return storage.ScopeEnviron
}

func (g *gceStorageProvider) Dynamic() bool {
	return true
}

func (g *gceStorageProvider) FilesystemSource(environConfig *config.Config, providerConfig *storage.Config) (storage.FilesystemSource, error) {
	return nil, errors.NotSupportedf("filesystems")
}

type gceVolumeSource struct {
	gce     gceConnection
	envName string // non-unique, informational only
	envUUID string
}

func (g *gceStorageProvider) VolumeSource(environConfig *config.Config, cfg *storage.Config) (storage.VolumeSource, error) {
	uuid, ok := environConfig.UUID()
	if !ok {
		return nil, errors.NotFoundf("environment UUID")
	}

	// Connect and authenticate.
	env, err := newEnviron(environConfig)
	if err != nil {
		return nil, errors.Trace(err)
	}

	source := &gceVolumeSource{
		gce:     env.gce,
		envName: environConfig.Name(),
		envUUID: uuid,
	}
	return source, nil
}

type instanceCache map[string]google.Instance

func (c instanceCache) update(gceClient gceConnection, ids ...string) error {
	if len(ids) == 1 {
		if _, ok := c[ids[0]]; ok {
			return nil
		}
	}
	idMap := make(map[string]int, len(ids))
	for _, id := range ids {
		idMap[id] = 0
	}
	instances, err := gceClient.Instances("", google.StatusRunning)
	if err != nil {
		return errors.Annotate(err, "querying instance details")
	}
	for _, instance := range instances {
		if _, ok := idMap[instance.ID]; !ok {
			continue
		}
		c[instance.ID] = instance
	}
	return nil
}

func (c instanceCache) get(id string) (google.Instance, error) {
	inst, ok := c[id]
	if !ok {
		return google.Instance{}, errors.Errorf("cannot attach to non-running instance %v", id)
	}
	return inst, nil
}

func (v *gceVolumeSource) CreateVolumes(params []storage.VolumeParams) (_ []storage.CreateVolumesResult, err error) {
	results := make([]storage.CreateVolumesResult, len(params))
	instanceIds := set.NewStrings()
	for i, p := range params {
		if err := v.ValidateVolumeParams(p); err != nil {
			results[i].Error = err
			continue
		}
		instanceIds.Add(string(p.Attachment.InstanceId))
	}

	instances := make(instanceCache)
	if instanceIds.Size() > 1 {
		if err := instances.update(v.gce, instanceIds.Values()...); err != nil {
			logger.Debugf("querying running instances: %v", err)
			// We ignore the error, because we don't want an invalid
			// InstanceId reference from one VolumeParams to prevent
			// the creation of another volume.
		}
	}

	for i, p := range params {
		if results[i].Error != nil {
			continue
		}
		volume, attachment, err := v.createOneVolume(p, instances)
		if err != nil {
			results[i].Error = err
			logger.Errorf("could not create one volume (or attach it): %v", err)
			continue
		}
		results[i].Volume = volume
		results[i].VolumeAttachment = attachment
	}
	return results, nil
}

// mibToGib converts mebibytes to gibibytes.
// AWS expects GiB, we work in MiB; round up
// to nearest GiB.
func mibToGib(m uint64) uint64 {
	return (m + 1023) / 1024
}

// gibToMib converts gibibytes to mebibytes.
func gibToMib(g uint64) uint64 {
	return g * 1024
}

func nameVolume(zone string) (string, error) {
	volumeUUID, err := utils.NewUUID()
	if err != nil {
		return "", errors.Trace(err)
	}
	// type-zone-uuid
	volumeName := fmt.Sprintf("%s--%s", zone, volumeUUID.String())
	return volumeName, nil
}

func (v *gceVolumeSource) createOneVolume(p storage.VolumeParams, instances instanceCache) (_ *storage.Volume, _ *storage.VolumeAttachment, err error) {
	var volumeName, zone string
	defer func() {
		if err == nil || volumeName == "" {
			return
		}
		if err := v.gce.RemoveDisk(zone, volumeName); err != nil {
			logger.Warningf("error cleaning up volume %v: %v", volumeName, err)
		}
	}()

	instId := string(p.Attachment.InstanceId)
	if err := instances.update(v.gce, instId); err != nil {
		return nil, nil, errors.Trace(err)
	}
	inst, err := instances.get(instId)
	if err != nil {
		// Can't create the volume without the instance,
		// because we need to know what its AZ is.
		return nil, nil, errors.Trace(err)
	}
	persistentType, ok := p.Attributes["type"].(string)
	if !ok {
		persistentType = google.DiskTypePersistentStandard
	}

	zone = inst.ZoneName
	volumeName, err = nameVolume(zone)
	if err != nil {
		return nil, nil, errors.Annotate(err, "cannot create a new volume name")
	}
	disk := google.DiskSpec{
		SizeHintGB:         mibToGib(p.Size),
		Name:               volumeName, // TODO(perrito666) this is wrong
		PersistentDiskType: persistentType,
	}

	gceDisks, err := v.gce.CreateDisks(zone, []google.DiskSpec{disk})
	if err != nil {
		return nil, nil, errors.Annotate(err, "cannot create disk")
	}
	if len(gceDisks) != 1 {
		return nil, nil, errors.New("unexpected number of disks created")
	}
	gceDisk := gceDisks[0]
	// TODO(perrito666) Tag, there are no tags in gce, how do we fix it?

	// Attachment
	var volumeAttachment *storage.VolumeAttachment

	attachedDisk, err := v.attachOneVolume(gceDisk.Name, "READ_WRITE", inst.ID)
	if err != nil {
		// This will be re-tried if attachment is nil here
		// by invoking AttachVolumes.
		logger.Errorf("attaching %v to %v: %v", gceDisk.Id, instId, err)
		volumeAttachment = nil
	} else {
		volumeAttachment = &storage.VolumeAttachment{
			p.Tag,
			p.Attachment.Machine,
			storage.VolumeAttachmentInfo{
				DeviceName: attachedDisk.DeviceName,
			},
		}
	}

	volume := storage.Volume{
		p.Tag,
		storage.VolumeInfo{
			VolumeId:   gceDisk.Name,
			Size:       gibToMib(uint64(gceDisk.SizeGb)),
			Persistent: true,
		},
	}

	return &volume, volumeAttachment, nil

}

func (v *gceVolumeSource) DestroyVolumes(volNames []string) ([]error, error) {
	var wg sync.WaitGroup
	wg.Add(len(volNames))
	results := make([]error, len(volNames))
	for i, volumeName := range volNames {
		go func(i int, volumeName string) {
			defer wg.Done()
			results[i] = v.destroyOneVolume(volumeName)
		}(i, volumeName)
	}
	wg.Wait()
	return results, nil
}

func parseVolumeId(volName string) (string, string, error) {
	idRest := strings.SplitN(volName, "--", 2)
	if len(idRest) != 2 {
		return "", "", errors.New(fmt.Sprintf("malformed volume id %q", volName))
	}
	zone := idRest[0]
	volumeUUID := idRest[1]
	return zone, volumeUUID, nil

}
func (v *gceVolumeSource) destroyOneVolume(volName string) error {
	zone, _, err := parseVolumeId(volName)
	if err != nil {
		return errors.Annotatef(err, "invalid volume id %q", volName)
	}
	if err := v.gce.RemoveDisk(zone, volName); err != nil {
		return errors.Annotatef(err, "cannot destroy volume %q", volName)
	}
	return nil

}

func (v *gceVolumeSource) ListVolumes() ([]string, error) {
	azs, err := v.gce.AvailabilityZones("")
	if err != nil {
		return nil, errors.Annotate(err, "cannot determine availability zones")
	}
	var volumes []string
	for _, zone := range azs {
		disks, err := v.gce.Disks(zone.Name())
		if err != nil {
			// maybe use available and status also.
			logger.Errorf("cannot get disks for %q zone", zone.Name())
			continue
		}
		for _, disk := range disks {
			volumes = append(volumes, disk.Name)
		}
	}
	return volumes, nil
}
func (v *gceVolumeSource) DescribeVolumes(volNames []string) ([]storage.DescribeVolumesResult, error) {
	results := make([]storage.DescribeVolumesResult, len(volNames))
	for i, vol := range volNames {
		res, err := v.describeOneVolume(vol)
		if err != nil {
			return nil, errors.Annotate(err, "cannot describe volumes")
		}
		results[i] = res
	}
	return results, nil
}

func (v *gceVolumeSource) describeOneVolume(volName string) (storage.DescribeVolumesResult, error) {
	zone, _, err := parseVolumeId(volName)
	if err != nil {
		return storage.DescribeVolumesResult{}, errors.Annotatef(err, "cannot describe %q", volName)
	}
	disk, err := v.gce.Disk(zone, volName)
	if err != nil {
		return storage.DescribeVolumesResult{}, errors.Annotatef(err, "cannot get volume %q", volName)
	}
	desc := storage.DescribeVolumesResult{
		&storage.VolumeInfo{
			Size:     gibToMib(uint64(disk.SizeGb)),
			VolumeId: disk.Name,
		},
		nil,
	}
	return desc, nil
}

func (v *gceVolumeSource) ValidateVolumeParams(params storage.VolumeParams) error {
	return nil
}

func (v *gceVolumeSource) AttachVolumes(attachParams []storage.VolumeAttachmentParams) ([]storage.AttachVolumesResult, error) {
	results := make([]storage.AttachVolumesResult, len(attachParams))
	for i, attachment := range attachParams {
		volumeName := attachment.VolumeId
		mode := "READ_WRITE"
		if attachment.ReadOnly {
			mode = "READ_ONLY"
		}
		instanceId := attachment.InstanceId
		attached, err := v.attachOneVolume(volumeName, mode, string(instanceId))
		if err != nil {
			logger.Errorf("could not attach %q to %q: %v", volumeName, instanceId, err)
			results[i].Error = err
			continue
		}
		results[i].VolumeAttachment = &storage.VolumeAttachment{
			attachment.Volume,
			attachment.Machine,
			storage.VolumeAttachmentInfo{
				DeviceName: attached.DeviceName,
			},
		}
	}
	return results, nil
}

func (v *gceVolumeSource) attachOneVolume(volumeName, mode, instanceId string) (*google.AttachedDisk, error) {
	zone, _, err := parseVolumeId(volumeName)
	if err != nil {
		return nil, errors.Annotate(err, "invalid volume name")
	}
	instanceDisks, err := v.gce.InstanceDisks(zone, instanceId)
	if err != nil {
		return nil, errors.Annotate(err, "cannot verify if the disk is already in the instance")
	}
	// Is it already attached?
	for _, disk := range instanceDisks {
		if disk.VolumeName == volumeName {
			return disk, nil
		}
	}

	attachment, err := v.gce.AttachDisk(zone, volumeName, instanceId, mode)
	if err != nil {
		return nil, errors.Annotate(err, "cannot attach volume")
	}
	return attachment, nil
}

func (v *gceVolumeSource) DetachVolumes(attachParams []storage.VolumeAttachmentParams) ([]error, error) {
	result := make([]error, len(attachParams))
	for i, volumeAttachment := range attachParams {
		result[i] = v.detachOneVolume(volumeAttachment)
	}
	return result, nil
}

func (v *gceVolumeSource) detachOneVolume(attachParam storage.VolumeAttachmentParams) error {
	instId := attachParam.InstanceId
	volumeName := attachParam.VolumeId
	zone, _, err := parseVolumeId(volumeName)
	if err != nil {
		return errors.Annotatef(err, "%q is not a valid volume id", volumeName)
	}
	return v.gce.DetachDisk(zone, string(instId), volumeName)
}
