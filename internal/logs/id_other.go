//go:build !unix

package logs

import "os"

type fileID struct {
	valid bool
}

func identify(os.FileInfo) fileID { return fileID{} }
