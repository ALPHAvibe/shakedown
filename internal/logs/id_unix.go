//go:build unix

package logs

import (
	"os"
	"syscall"
)

type fileID struct {
	ino   uint64
	valid bool
}

func identify(info os.FileInfo) fileID {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileID{}
	}
	return fileID{ino: stat.Ino, valid: true}
}
