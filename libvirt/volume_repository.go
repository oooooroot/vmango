package libvirt

import (
	"fmt"
	"subuk/vmango/compute"
	"subuk/vmango/util"

	"github.com/libvirt/libvirt-go-xml"

	libvirt "github.com/libvirt/libvirt-go"
)

type VolumeRepository struct {
	pool *ConnectionPool
}

func NewVolumeRepository(pool *ConnectionPool) *VolumeRepository {
	return &VolumeRepository{pool: pool}
}

func (repo *VolumeRepository) virVolumeToVolume(pool *libvirt.StoragePool, virVolume *libvirt.StorageVol) (*compute.Volume, error) {
	virVolumeXml, err := virVolume.GetXMLDesc(0)
	if err != nil {
		return nil, util.NewError(err, "cannot get volume info")
	}
	virVolumeConfig := &libvirtxml.StorageVolume{}
	if err := virVolumeConfig.Unmarshal(virVolumeXml); err != nil {
		return nil, util.NewError(err, "cannot unmarshal volume xml")
	}
	poolXml, err := pool.GetXMLDesc(0)
	if err != nil {
		return nil, util.NewError(err, "cannot get storage pool xml")
	}
	poolConfig := &libvirtxml.StoragePool{}
	if err := poolConfig.Unmarshal(poolXml); err != nil {
		return nil, util.NewError(err, "cannot unmarshal storage pool xml")
	}

	volume := &compute.Volume{}
	volume.Type = virVolumeConfig.Type
	volume.Path = virVolumeConfig.Target.Path
	volume.Pool = poolConfig.Name
	volume.Format = compute.FormatUnknown

	switch poolConfig.Type {
	case "logical":
		volume.Format = compute.FormatRaw
	}

	if virVolumeConfig.Target != nil && virVolumeConfig.Target.Format != nil {
		switch virVolumeConfig.Target.Format.Type {
		case "qcow2":
			volume.Format = compute.FormatQcow2
		case "raw":
			volume.Format = compute.FormatRaw
		}
	}

	switch virVolumeConfig.Capacity.Unit {
	default:
		return nil, fmt.Errorf("unknown volume capacity unit '%s'", virVolumeConfig.Capacity.Unit)
	case "bytes":
		volume.Size = virVolumeConfig.Capacity.Value / 1024 / 1024
	}
	return volume, nil
}

func (repo *VolumeRepository) fetchAttachedVm(conn *libvirt.Connect, volumes []*compute.Volume) error {
	domains, err := conn.ListAllDomains(0)
	if err != nil {
		return util.NewError(err, "cannot list domains")
	}
	for _, domain := range domains {
		domainXml, err := domain.GetXMLDesc(libvirt.DOMAIN_XML_MIGRATABLE)
		if err != nil {
			return util.NewError(err, "cannot get domain xml description")
		}
		domainConfig := &libvirtxml.Domain{}
		if err := domainConfig.Unmarshal(domainXml); err != nil {
			return util.NewError(err, "cannot unmarshal domain xml")
		}
		for _, diskConfig := range domainConfig.Devices.Disks {
			attachedVolume := VirtualMachineAttachedVolumeFromDomainDiskConfig(diskConfig)
			if attachedVolume == nil {
				continue
			}
			for _, volume := range volumes {
				if volume.Path == attachedVolume.Path {
					volume.AttachedTo = domainConfig.Name
				}
			}
		}
	}
	return nil
}

func (repo *VolumeRepository) Get(path string) (*compute.Volume, error) {
	conn, err := repo.pool.Acquire()
	if err != nil {
		return nil, util.NewError(err, "cannot acquire connection")
	}
	defer repo.pool.Release(conn)

	virVolume, err := conn.LookupStorageVolByPath(path)
	if err != nil {
		return nil, util.NewError(err, "cannot lookup volume by path %s", path)
	}
	pool, err := virVolume.LookupPoolByVolume()
	if err != nil {
		return nil, util.NewError(err, "cannot lookup pool for volume")
	}
	volume, err := repo.virVolumeToVolume(pool, virVolume)
	if err != nil {
		return nil, util.NewError(err, "cannot parse volume")
	}
	if err := repo.fetchAttachedVm(conn, []*compute.Volume{volume}); err != nil {
		return nil, util.NewError(err, "cannot fetch attached vm")
	}
	return volume, nil
}

func (repo *VolumeRepository) List() ([]*compute.Volume, error) {
	conn, err := repo.pool.Acquire()
	if err != nil {
		return nil, util.NewError(err, "cannot acquire connection")
	}
	defer repo.pool.Release(conn)

	pools, err := conn.ListAllStoragePools(0)
	if err != nil {
		return nil, util.NewError(err, "cannot list storage pools")
	}

	volumes := []*compute.Volume{}
	for _, pool := range pools {
		virVolumes, err := pool.ListAllStorageVolumes(0)
		if err != nil {
			return nil, util.NewError(err, "cannot list storage volumes")
		}
		for _, virVolume := range virVolumes {
			volume, err := repo.virVolumeToVolume(&pool, &virVolume)
			if err != nil {
				return nil, util.NewError(err, "cannot parse libvirt volume")
			}
			volumes = append(volumes, volume)
		}
	}
	if err := repo.fetchAttachedVm(conn, volumes); err != nil {
		return nil, util.NewError(err, "cannot fetch attached vm")
	}
	return volumes, nil
}

func (repo *VolumeRepository) Pools() ([]*compute.VolumePool, error) {
	conn, err := repo.pool.Acquire()
	if err != nil {
		return nil, util.NewError(err, "cannot acquire connection")
	}
	defer repo.pool.Release(conn)

	virPools, err := conn.ListAllStoragePools(0)
	if err != nil {
		return nil, util.NewError(err, "cannot list storage pools")
	}
	volumePools := []*compute.VolumePool{}
	for _, virPool := range virPools {
		virPoolXml, err := virPool.GetXMLDesc(0)
		if err != nil {
			return nil, util.NewError(err, "cannot get pool name")
		}
		virPoolConfig := &libvirtxml.StoragePool{}
		if err := virPoolConfig.Unmarshal(virPoolXml); err != nil {
			return nil, util.NewError(err, "cannot unmarshal volume pool xml")
		}
		volumePool := &compute.VolumePool{
			Name: virPoolConfig.Name,
		}
		// Size: virPoolConfig.Capacity
		if virPoolConfig.Capacity != nil {
			volumePool.Size = ParseLibvirtSizeToMegabytes(virPoolConfig.Capacity.Unit, virPoolConfig.Capacity.Value)
		}
		if virPoolConfig.Allocation != nil {
			volumePool.Used = ParseLibvirtSizeToMegabytes(virPoolConfig.Allocation.Unit, virPoolConfig.Allocation.Value)
		}
		if virPoolConfig.Available != nil {
			volumePool.Free = ParseLibvirtSizeToMegabytes(virPoolConfig.Available.Unit, virPoolConfig.Available.Value)
		}
		volumePools = append(volumePools, volumePool)
	}
	return volumePools, nil
}

func (repo *VolumeRepository) Create(poolName, volumeName string, volumeFormat compute.VolumeFormat, size uint64) (*compute.Volume, error) {
	conn, err := repo.pool.Acquire()
	if err != nil {
		return nil, util.NewError(err, "cannot acquire connection")
	}
	defer repo.pool.Release(conn)

	virPool, err := conn.LookupStoragePoolByName(poolName)
	if err != nil {
		return nil, util.NewError(err, "cannot lookup libvirt pool")
	}

	virVolumeConfig := &libvirtxml.StorageVolume{}
	virVolumeConfig.Name = volumeName
	virVolumeConfig.Capacity = &libvirtxml.StorageVolumeSize{
		Unit:  "MiB",
		Value: size,
	}
	if volumeFormat == compute.FormatQcow2 {
		virVolumeConfig.Target = &libvirtxml.StorageVolumeTarget{
			Format: &libvirtxml.StorageVolumeTargetFormat{
				Type: "qcow2",
			},
		}
	}

	virVolumeXml, err := virVolumeConfig.Marshal()
	if err != nil {
		return nil, util.NewError(err, "cannot marshal libvirt volume config")
	}
	virVolCreateFlags := libvirt.StorageVolCreateFlags(0)
	if volumeFormat == compute.FormatQcow2 {
		virVolCreateFlags |= libvirt.STORAGE_VOL_CREATE_PREALLOC_METADATA
	}

	virVolume, err := virPool.StorageVolCreateXML(virVolumeXml, virVolCreateFlags)
	if err != nil {
		return nil, util.NewError(err, "cannot create volume")
	}
	return repo.virVolumeToVolume(virPool, virVolume)
}

func (repo *VolumeRepository) Delete(path string) error {
	conn, err := repo.pool.Acquire()
	if err != nil {
		return util.NewError(err, "cannot acquire connection")
	}
	defer repo.pool.Release(conn)

	virVolume, err := conn.LookupStorageVolByPath(path)
	if err != nil {
		return util.NewError(err, "cannot lookup storage volume")
	}
	if err := virVolume.Delete(libvirt.STORAGE_VOL_DELETE_NORMAL); err != nil {
		return util.NewError(err, "cannot delete volume")
	}
	return nil
}
