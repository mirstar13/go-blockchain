//go:build !windows

package wallet

import "os"

const RequiresEncryption = false

func privateFile(info os.FileInfo) bool {
	return info.Mode().IsRegular() && info.Mode().Perm()&0077 == 0
}
