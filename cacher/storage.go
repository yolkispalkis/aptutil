package cacher

import (
	"container/heap"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/cybozu-go/log"
	"github.com/pkg/errors"

	"github.com/yolkispalkis/aptutil/apt"
)

const (
	fileSuffix = ".cache"
)

var (
	// ErrNotFound is returned by Storage.Lookup for non-existing items.
	ErrNotFound = errors.New("not found")

	// ErrBadPath is returned by Storage.Insert if path is bad
	ErrBadPath = errors.New("bad path")
)

// entry represents an item in the cache.
type entry struct {
	*apt.FileInfo

	// for container/heap.
	// atime is used as priorities.
	atime int64
	index int
}

// FilePath returns the filename of the entry.
func (e *entry) FilePath() string {
	return e.Path() + fileSuffix
}

// Storage stores cache items in local file system.
//
// Cached items will be removed in LRU fashion when the total size of
// items exceeds the capacity.
type Storage struct {
	dir      string // directory for cache items
	capacity uint64

	mu     sync.Mutex
	used   uint64
	cache  map[string]*entry
	lru    entryHeap
	lclock int64 // ditto
}

// NewStorage creates a Storage.
//
// dir is the directory for cached items.
// capacity is the maximum total size (bytes) of items in the cache.
// If capacity is zero, items will not be evicted.
// Non-existing directories will be created (insufficient permission result in panic)
func NewStorage(dir string, capacity uint64) *Storage {
	if !filepath.IsAbs(dir) {
		panic("dir must be an absolute path")
	}

	if _, err := os.Stat(dir); os.IsNotExist(err) {
		err = os.MkdirAll(dir, 0755)
		if err != nil {
			panic("Storage.NewStorage: failed to create " + dir)
		}
	}

	return &Storage{
		dir:      dir,
		cache:    make(map[string]*entry),
		capacity: capacity,
	}
}

// entryHeap implements heap.Interface.
type entryHeap []*entry

// Len implements heap.Interface.
func (h entryHeap) Len() int {
	return len(h)
}

// Less implements heap.Interface.
func (h entryHeap) Less(i, j int) bool {
	return h[i].atime < h[j].atime
}

// Swap implements heap.Interface.
func (h entryHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

// Push implements heap.Interface.
func (h *entryHeap) Push(x interface{}) {
	e := x.(*entry)
	n := len(*h)
	e.index = n
	*h = append(*h, e)
}

// Pop implements heap.Interface.
func (h *entryHeap) Pop() interface{} {
	n := len(*h)
	e := (*h)[n-1]
	e.index = -1 // for safety
	*h = (*h)[0 : n-1]
	return e
}

// maint removes unused items from cache until used < capacity.
// cm.mu lock must be acquired beforehand.
func (cm *Storage) maint() {
	for cm.capacity > 0 && cm.used > cm.capacity {
		e := heap.Pop(&cm.lru).(*entry)
		delete(cm.cache, e.Path())
		cm.used -= e.Size()
		if err := os.Remove(filepath.Join(cm.dir, e.FilePath())); err != nil {
			log.Warn("Storage.maint", map[string]interface{}{
				"error": err.Error(),
			})
		}
		log.Info("removed", map[string]interface{}{
			"path": e.Path(),
		})
	}
}

func readData(path string) ([]byte, error) {
	return ioutil.ReadFile(path)
}

// Load loads existing items in filesystem.
func (cm *Storage) Load() error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	wf := func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		subpath, err := filepath.Rel(cm.dir, path)
		if err != nil {
			return err
		}
		if filepath.Ext(subpath) != fileSuffix {
			return nil
		}
		subpath = subpath[:len(subpath)-len(fileSuffix)]
		if _, ok := cm.cache[subpath]; ok {
			return nil
		}

		size := uint64(info.Size())
		e := &entry{
			// delay calculation of checksums.
			FileInfo: apt.MakeFileInfoNoChecksum(subpath, size),
			atime:    atomic.AddInt64(&cm.lclock, 1),
		}
		cm.used += size
		cm.lru.Push(e)
		cm.cache[subpath] = e
		log.Debug("Storage.Load", map[string]interface{}{
			"path": subpath,
		})
		return nil
	}

	if err := filepath.Walk(cm.dir, wf); err != nil {
		return err
	}
	heap.Init(&cm.lru)

	cm.maint()

	return nil
}

// TempFile creates a new temporary file
// in the directory specified in Storage,
// opens the file for reading and writing,
// and returns the resulting *os.File.
func (cm *Storage) TempFile() (*os.File, error) {
	return ioutil.TempFile(cm.dir, "_tmp")
}

// Insert inserts or updates a cache item.
//
// fi.Path() must be as clean as filepath.Clean() and
// must not be filepath.IsAbs().
func (cm *Storage) Insert(filename string, fi *apt.FileInfo) error {
	p := fi.Path()
	switch {
	case p != filepath.Clean(p):
		return ErrBadPath
	case filepath.IsAbs(p):
		return ErrBadPath
	case p == ".":
		return ErrBadPath
	}

	destpath := filepath.Join(cm.dir, p+fileSuffix)
	dirpath := filepath.Dir(destpath)

	_, err := os.Stat(dirpath)
	switch {
	case os.IsNotExist(err):
		err = os.MkdirAll(dirpath, 0755)
		if err != nil {
			return err
		}
	case err != nil:
		return err
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()

	if existing, ok := cm.cache[p]; ok {
		err = os.Remove(destpath)
		if err != nil {
			if !os.IsNotExist(err) {
				return err
			}
			log.Warn("cache file was removed already", map[string]interface{}{
				"path": p,
			})
		}
		cm.used -= existing.Size()
		heap.Remove(&cm.lru, existing.index)
		delete(cm.cache, p)
		if log.Enabled(log.LvDebug) {
			log.Debug("deleted existing item", map[string]interface{}{
				"path": p,
			})
		}
	}

	err = os.Link(filename, destpath)
	if err != nil {
		return err
	}

	e := &entry{
		FileInfo: fi,
		atime:    atomic.AddInt64(&cm.lclock, 1),
	}
	cm.used += fi.Size()
	heap.Push(&cm.lru, e)
	cm.cache[p] = e

	cm.maint()

	return nil
}

func calcChecksum(dir string, e *entry) error {
	if e.FileInfo.HasChecksum() {
		return nil
	}

	data, err := readData(filepath.Join(dir, e.FilePath()))
	if err != nil {
		return err
	}
	e.FileInfo.CalcChecksums(data)
	return nil
}

// Lookup looks up an item in the cache.
// If no item matching fi is found, ErrNotFound is returned.
//
// The caller is responsible to close the returned os.File.
func (cm *Storage) Lookup(fi *apt.FileInfo) (*os.File, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	e, ok := cm.cache[fi.Path()]
	if !ok {
		return nil, ErrNotFound
	}

	// delayed checksum calculation
	err := calcChecksum(cm.dir, e)
	if err != nil {
		return nil, err
	}

	if !fi.Same(e.FileInfo) {
		// checksum mismatch
		return nil, ErrNotFound
	}

	e.atime = atomic.AddInt64(&cm.lclock, 1)
	heap.Fix(&cm.lru, e.index)
	return os.Open(filepath.Join(cm.dir, e.FilePath()))
}

// ListAll returns a list of *apt.FileInfo for all cached items.
func (cm *Storage) ListAll() []*apt.FileInfo {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	l := make([]*apt.FileInfo, cm.lru.Len())
	for i, e := range cm.lru {
		l[i] = e.FileInfo
	}
	return l
}

// Delete deletes an item from the cache.
func (cm *Storage) Delete(p string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	e, ok := cm.cache[p]
	if !ok {
		return nil
	}

	err := os.Remove(filepath.Join(cm.dir, e.FilePath()))
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		log.Warn("cached file was already removed", map[string]interface{}{
			"path": p,
		})
	}

	cm.used -= e.Size()
	heap.Remove(&cm.lru, e.index)
	delete(cm.cache, p)
	log.Info("deleted item", map[string]interface{}{
		"path": p,
	})
	return nil
}
