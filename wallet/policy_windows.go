package wallet

import "os"

// RequiresEncryption is true where plaintext wallet operations are unsupported.
const RequiresEncryption = true

// Windows ACLs, rather than Unix permission bits, control file access. Users
// must protect key and passphrase paths with appropriate ACLs.
func privateFile(info os.FileInfo) bool { return info.Mode().IsRegular() }
