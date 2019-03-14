package storageman

import (
	"context"
	"fmt"
	"io/ioutil"
	"path"
	"strings"
	"time"

	"yunion.io/x/log"
	"yunion.io/x/pkg/util/timeutils"

	"yunion.io/x/onecloud/pkg/cloudcommon/consts/storagetypes"
	"yunion.io/x/onecloud/pkg/hostman/hostutils"
	"yunion.io/x/onecloud/pkg/hostman/options"
	"yunion.io/x/onecloud/pkg/mcclient"
	"yunion.io/x/onecloud/pkg/util/fileutils2"
	"yunion.io/x/onecloud/pkg/util/procutils"
)

const MINIMAL_FREE_SPACE = 128

type SStorageManager struct {
	host hostutils.IHost

	Storages     []IStorage
	AgentStorage IStorage

	LocalStorageImagecacheManager IImageCacheManger
	// AgentStorageImagecacheManager IImageCacheManger

	RbdStorageImagecacheManagers map[string]IImageCacheManger
	NfsStorageImagecacheManagers map[string]IImageCacheManger
}

func NewStorageManager(host hostutils.IHost) (*SStorageManager, error) {
	var (
		ret = &SStorageManager{
			host:     host,
			Storages: make([]IStorage, 0),
		}
		allFull = true
	)

	for i, d := range options.HostOptions.LocalImagePath {
		s := NewLocalStorage(ret, d, i)
		if s.Accessible() {
			ret.Storages = append(ret.Storages, s)
			if allFull && s.GetFreeSizeMb() > MINIMAL_FREE_SPACE {
				allFull = false
			}
		}
	}

	for _, d := range options.HostOptions.SharedStorages {
		s := ret.NewSharedStorageInstance(d, "")
		if s != nil {
			ret.Storages = append(ret.Storages, s)
			allFull = false
		}
	}

	if allFull {
		return nil, fmt.Errorf("Not enough storage space!")
	}

	if err := ret.initLocalStorageImagecache(); err != nil {
		return nil, fmt.Errorf("Init Local storage image cache failed: %s", err)
	}

	return ret, nil
}

func (s *SStorageManager) Remove(storage IStorage) {
	if storage.StorageType() == storagetypes.STORAGE_NFS {
		delete(s.NfsStorageImagecacheManagers, storage.GetStoragecacheId())
	} else if storage.StorageType() == storagetypes.STORAGE_RBD {
		delete(s.RbdStorageImagecacheManagers, storage.GetStoragecacheId())
	}
	for index, iS := range s.Storages {
		if iS.GetId() == storage.GetId() {
			s.Storages = append(s.Storages[:index], s.Storages[index+1:]...)
			break
		}
	}
}

func (s *SStorageManager) GetZone() string {
	return s.host.GetZone()
}

func (s *SStorageManager) GetHostId() string {
	return s.host.GetHostId()
}

func (s *SStorageManager) GetMediumType() string {
	return s.host.GetMediumType()
}

func (s *SStorageManager) getLeasedUsedLocalStorage(cacheDir string, limit int) (string, error) {
	var (
		maxFree    int
		spath      string
		maxStorage IStorage
	)

	for _, storage := range s.Storages {

		if _, ok := storage.(*SLocalStorage); ok {
			cachePath := path.Join(storage.GetPath(), cacheDir)
			if fileutils2.Exists(cachePath) {
				spath = cachePath
				break
			}
			free := storage.GetFreeSizeMb()
			if maxFree < free {
				maxFree = free
				maxStorage = storage
			}
		}
	}

	if len(spath) == 0 {
		if maxFree >= limit*1024 {
			spath = path.Join(maxStorage.GetPath(), cacheDir)
		} else {
			return "", fmt.Errorf("No local storage has free space larger than %dGB", limit)
		}
	}
	return spath, nil
}

func (s *SStorageManager) initLocalStorageImagecache() error {
	var (
		cacheDir  = "image_cache"
		cachePath = options.HostOptions.ImageCachePath
		limit     = options.HostOptions.ImageCacheLimit
	)

	if len(cachePath) == 0 {
		var err error
		cachePath, err = s.getLeasedUsedLocalStorage(cacheDir, limit)
		if err != nil {
			return err
		}
	}
	if len(cachePath) > 0 {
		s.LocalStorageImagecacheManager = NewLocalImageCacheManager(s, cachePath, limit, true, "")
		return nil
	} else {
		return fmt.Errorf("Cannot allocate image cache storage")
	}
}

// func (s *SStorageManager) initAgentStorageImagecache() {
// 	s.AgentStorageImagecacheManager = NewAgentImageCacheManager(s)
// }

// func (s *SStorageManager) initAgentStorage() error {
// 	var cacheDir = "agent_tmp"
// 	var spath = options.HostOptions.AgentTempPath
// 	var limit = options.HostOptions.AgentTempLimit
// 	if len(spath) == 0 {
// 		var err error
// 		spath, err = s.getLeasedUsedLocalStorage(cacheDir, limit)
// 		if err != nil {
// 			return err
// 		}
// 	}
// 	if len(spath) != nil {
// 		// TODO: NewAgentStorage
// 		s.AgentStorage = NewAgentStorage(s, spath)
// 	} else {
// 		return fmt.Errorf("Cannot allocate agent storage")
// 	}
// }

// func (s *SStorageManager) AddNfsStorage(storagecacheId, cachePath string) {
// 	if len(cachePath) == 0 {
// 		return
// 	}
// 	if s.NfsStorageImagecacheManagers == nil {
// 		s.NfsStorageImagecacheManagers = make(map[string]IImageCacheManger, 0)
// 	}
// 	s.NfsStorageImagecacheManagers[storagecacheId] = NewLocalImageCacheManager(s, cachePath,
// 		options.HostOptions.ImageCacheLimit, true, storagecacheId)
// }

func (s *SStorageManager) GetStorage(storageId string) IStorage {
	for _, storage := range s.Storages {
		if storage.GetId() == storageId {
			return storage
		}
	}
	return nil
}

func (s *SStorageManager) GetStorageDisk(storageId, diskId string) IDisk {
	if storage := s.GetStorage(storageId); storage != nil {
		return storage.GetDiskById(diskId)
	}
	return nil
}

func (s *SStorageManager) GetStorageByPath(sPath string) IStorage {
	for _, storage := range s.Storages {
		if storage.GetPath() == sPath {
			return storage
		}
	}
	return nil
}

func (s *SStorageManager) GetDiskByPath(diskPath string) IDisk {
	pos := strings.LastIndex(diskPath, "/")
	sPath := diskPath[:pos]
	diskId := diskPath[pos+1:]
	pos = strings.LastIndex(diskId, ".")
	if pos > 0 {
		diskId = diskId[:pos]
	}
	storage := s.GetStorageByPath(sPath)
	if storage != nil {
		return storage.GetDiskById(diskId)
	}
	return nil
}

func (s *SStorageManager) GetTotalCapacity() int {
	var capa = 0
	for _, s := range s.Storages {
		capa += s.GetCapacity()
	}
	return capa
}

func (s *SStorageManager) GetStoragecacheById(scId string) IImageCacheManger {
	if s.LocalStorageImagecacheManager.GetId() == scId {
		return s.LocalStorageImagecacheManager
	}
	if sc, ok := s.NfsStorageImagecacheManagers[scId]; ok {
		return sc
	}
	if sc, ok := s.RbdStorageImagecacheManagers[scId]; ok {
		return sc
	}
	return nil
}

func (s *SStorageManager) NewSharedStorageInstance(mountPoint, storageType string) IStorage {
	return NewStorage(s, mountPoint, storageType)
}

func (s *SStorageManager) InitSharedStorageImageCache(storageType, storagecacheId, imagecachePath string, storage IStorage) {
	if storageType == storagetypes.STORAGE_NFS {
		s.InitNfsStorageImagecache(storagecacheId, imagecachePath)
	} else if storageType == storagetypes.STORAGE_RBD {
		if rbdStorage := s.GetStoragecacheById(storagecacheId); rbdStorage == nil {
			// Done
			s.AddRbdStorageImagecache(imagecachePath, storage, storagecacheId)
		}
	}
}

func (s *SStorageManager) InitNfsStorageImagecache(storagecacheId, path string) {
	if len(path) == 0 {
		return
	}
	if s.NfsStorageImagecacheManagers == nil {
		s.NfsStorageImagecacheManagers = map[string]IImageCacheManger{}
	}
	if _, ok := s.NfsStorageImagecacheManagers[storagecacheId]; !ok {
		s.NfsStorageImagecacheManagers[storagecacheId] = NewLocalImageCacheManager(s, path, options.HostOptions.ImageCacheLimit, true, storagecacheId)
	}
}

func (s *SStorageManager) AddRbdStorageImagecache(imagecachePath string, storage IStorage, storagecacheId string) {
	if s.RbdStorageImagecacheManagers == nil {
		s.RbdStorageImagecacheManagers = map[string]IImageCacheManger{}
	}
	if _, ok := s.RbdStorageImagecacheManagers[storagecacheId]; !ok {
		if imagecache := NewImageCacheManager(s, imagecachePath, storage, storagecacheId, storagetypes.STORAGE_RBD); imagecache != nil {
			s.RbdStorageImagecacheManagers[storagecacheId] = imagecache
			return
		}
		log.Errorf("failed init storagecache %s for storage %s", storagecacheId, storage.GetStorageName())
	}
}

var storageManager *SStorageManager

func GetManager() *SStorageManager {
	return storageManager
}

func Manager() *SStorageManager {
	return storageManager
}

func Init(host hostutils.IHost) error {
	var err error
	storageManager, err = NewStorageManager(host)
	return err
}

func Stop() {
	// pass do nothing
}

func cleanDailyFiles(storagePath, subDir string, keepDay int) {
	recycleDir := path.Join(storagePath, subDir)
	if !fileutils2.Exists(recycleDir) {
		return
	}

	// before mark should be deleted
	markTime := timeutils.UtcNow().Add(time.Hour * 24 * -1 * time.Duration(keepDay))
	files, err := ioutil.ReadDir(recycleDir)
	if err != nil {
		log.Errorln(err)
		return
	}

	for _, file := range files {
		date, err := timeutils.ParseTimeStr(file.Name())
		if err != nil {
			log.Errorln(err)
			continue
		}
		if date.Before(markTime) {
			log.Infof("Real delete %s", file)
			subDirPath := path.Join(recycleDir, file.Name())
			procutils.NewCommand("rm", "-rf", subDirPath)
		}
	}
}

func CleanRecycleDiskfiles(ctx context.Context, userCred mcclient.TokenCredential, isStart bool) {
	for _, d := range options.HostOptions.LocalImagePath {
		cleanDailyFiles(d, _RECYCLE_BIN_, options.HostOptions.RecycleDiskfileKeepDays)
		cleanDailyFiles(d, _IMGSAVE_BACKUPS_, options.HostOptions.RecycleDiskfileKeepDays)
	}
}
