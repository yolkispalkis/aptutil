package apt

import (
	"bytes"
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"io"
	"path"
	"time"

	"github.com/minio/sha256-simd"
	"github.com/pkg/errors"
)

// FileInfo is a set of meta data of a file.
type FileInfo struct {
	path         string
	size         uint64
	md5sum       []byte    // nil means no MD5 checksum to be checked.
	sha1sum      []byte    // nil means no SHA1 ...
	sha256sum    []byte    // nil means no SHA256 ...
	lastModified time.Time // Всратая переменная ...
}

// GetLastModified returns the time when the file was last modified.
func (fi *FileInfo) GetLastModified() time.Time {
	return fi.lastModified
}

// SetLastModified sets the time when the file was last modified.
func (fi *FileInfo) SetLastModified(t time.Time) {
	fi.lastModified = t
}

// Same returns true if t has the same checksum values.
func (fi *FileInfo) Same(t *FileInfo) bool {
	if fi == t {
		return true
	}
	if fi.path != t.path {
		return false
	}
	if fi.size != t.size {
		return false
	}
	if fi.md5sum != nil && bytes.Compare(fi.md5sum, t.md5sum) != 0 {
		return false
	}
	if fi.sha1sum != nil && bytes.Compare(fi.sha1sum, t.sha1sum) != 0 {
		return false
	}
	if fi.sha256sum != nil && bytes.Compare(fi.sha256sum, t.sha256sum) != 0 {
		return false
	}
	return true
}

// Path returns the indentifying path string of the file.
func (fi *FileInfo) Path() string {
	return fi.path
}

// Size returns the number of bytes of the file body.
func (fi *FileInfo) Size() uint64 {
	return fi.size
}

// HasChecksum returns true if fi has checksums.
func (fi *FileInfo) HasChecksum() bool {
	return fi.md5sum != nil
}

// CalcChecksums calculates checksums and stores them in fi.
func (fi *FileInfo) CalcChecksums(data []byte) {
	md5sum := md5.Sum(data)
	sha1sum := sha1.Sum(data)
	sha256sum := sha256.Sum256(data)
	fi.size = uint64(len(data))
	fi.md5sum = md5sum[:]
	fi.sha1sum = sha1sum[:]
	fi.sha256sum = sha256sum[:]
}

// AddPrefix creates a new FileInfo by prepending prefix to the path.
func (fi *FileInfo) AddPrefix(prefix string) *FileInfo {
	return &FileInfo{
		path:         path.Join(path.Clean(prefix), fi.path),
		size:         fi.size,
		md5sum:       fi.md5sum,
		sha1sum:      fi.sha1sum,
		sha256sum:    fi.sha256sum,
		lastModified: fi.lastModified,
	}
}

// MD5SumPath returns the filepath for "by-hash" with md5 checksum.
// If fi has no checksum, an empty string will be returned.
func (fi *FileInfo) MD5SumPath() string {
	if fi.md5sum == nil {
		return ""
	}
	return path.Join(path.Dir(fi.path),
		"by-hash",
		"MD5Sum",
		hex.EncodeToString(fi.md5sum))
}

// SHA1Path returns the filepath for "by-hash" with sha1 checksum.
// If fi has no checksum, an empty string will be returned.
func (fi *FileInfo) SHA1Path() string {
	if fi.sha1sum == nil {
		return ""
	}
	return path.Join(path.Dir(fi.path),
		"by-hash",
		"SHA1",
		hex.EncodeToString(fi.sha1sum))
}

// SHA256Path returns the filepath for "by-hash" with sha256 checksum.
// If fi has no checksum, an empty string will be returned.
func (fi *FileInfo) SHA256Path() string {
	if fi.sha256sum == nil {
		return ""
	}
	return path.Join(path.Dir(fi.path),
		"by-hash",
		"SHA256",
		hex.EncodeToString(fi.sha256sum))
}

type fileInfoJSON struct {
	Path         string `json:"Path"`
	Size         int64  `json:"Size"`
	MD5Sum       string `json:"MD5Sum,omitempty"`
	SHA1Sum      string `json:"SHA1Sum,omitempty"`
	SHA256Sum    string `json:"SHA256Sum,omitempty"`
	LastModified string `json:"LastModified,omitempty"`
}

// MarshalJSON implements json.Marshaler
func (fi *FileInfo) MarshalJSON() ([]byte, error) {
	fij := fileInfoJSON{
		Path: fi.path,
		Size: int64(fi.size),
	}
	if fi.md5sum != nil {
		fij.MD5Sum = hex.EncodeToString(fi.md5sum)
	}
	if fi.sha1sum != nil {
		fij.SHA1Sum = hex.EncodeToString(fi.sha1sum)
	}
	if fi.sha256sum != nil {
		fij.SHA256Sum = hex.EncodeToString(fi.sha256sum)
	}
	if !fi.lastModified.IsZero() {
		fij.LastModified = fi.lastModified.Format(time.RFC1123)
	}
	return json.Marshal(&fij)
}

// UnmarshalJSON implements json.Unmarshaler
func (fi *FileInfo) UnmarshalJSON(data []byte) error {
	var fij fileInfoJSON
	if err := json.Unmarshal(data, &fij); err != nil {
		return err
	}
	fi.path = fij.Path
	fi.size = uint64(fij.Size)
	if fij.MD5Sum != "" {
		md5sum, err := hex.DecodeString(fij.MD5Sum)
		if err != nil {
			return errors.Wrap(err, "UnmarshalJSON for "+fij.Path)
		}
		fi.md5sum = md5sum
	}
	if fij.SHA1Sum != "" {
		sha1sum, err := hex.DecodeString(fij.SHA1Sum)
		if err != nil {
			return errors.Wrap(err, "UnmarshalJSON for "+fij.Path)
		}
		fi.sha1sum = sha1sum
	}
	if fij.SHA256Sum != "" {
		sha256sum, err := hex.DecodeString(fij.SHA256Sum)
		if err != nil {
			return errors.Wrap(err, "UnmarshalJSON for "+fij.Path)
		}
		fi.sha256sum = sha256sum
	}

	if fij.LastModified != "" {
		t, err := time.Parse(time.RFC1123, fij.LastModified)
		if err == nil {
			fi.lastModified = t
		}
	}
	fi.md5sum = md5sum
	fi.sha1sum = sha1sum
	fi.sha256sum = sha256sum
	return nil
}

// CopyWithFileInfo copies from src to dst until either EOF is reached
// on src or an error occurs, and returns FileInfo calculated while copying.
func CopyWithFileInfo(dst io.Writer, src io.Reader, p string) (*FileInfo, error) {
	md5hash := md5.New()
	sha1hash := sha1.New()
	sha256hash := sha256.New()

	w := io.MultiWriter(md5hash, sha1hash, sha256hash, dst)
	n, err := io.Copy(w, src)
	if err != nil {
		return nil, err
	}

	return &FileInfo{
		path:      p,
		size:      uint64(n),
		md5sum:    md5hash.Sum(nil),
		sha1sum:   sha1hash.Sum(nil),
		sha256sum: sha256hash.Sum(nil),
	}, nil
}

// MakeFileInfoNoChecksum constructs a FileInfo without calculating checksums.
func MakeFileInfoNoChecksum(path string, size uint64) *FileInfo {
	return &FileInfo{
		path: path,
		size: size,
	}
}
