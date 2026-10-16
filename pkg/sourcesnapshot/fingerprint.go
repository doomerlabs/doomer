package sourcesnapshot

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
)

// TreeHashVersion names the byte-level identity contract shared with SaaS.
const TreeHashVersion = "doomer-source-tree-v1"

// TreeHash identifies a validated, path-sorted tree independently of commits,
// refs, JSON serialization, timestamps, and the review's baseline. Both clean
// and dirty captures use this same identity. Hashes include executable modes.
func TreeHash(entries []Entry) string {
	h := sha256.New()
	h.Write([]byte(TreeHashVersion + "\x00"))
	var size [4]byte
	for _, entry := range entries {
		binary.BigEndian.PutUint32(size[:], uint32(len(entry.Path)))
		h.Write(size[:])
		h.Write([]byte(entry.Path))
		h.Write([]byte(entry.Mode))
		digest, _ := hex.DecodeString(entry.Hash) // Entries have already been validated.
		h.Write(digest)
	}
	return hex.EncodeToString(h.Sum(nil))
}
