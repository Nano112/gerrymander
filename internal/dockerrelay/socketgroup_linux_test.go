//go:build linux

package dockerrelay

import "testing"

// acl builds a system.posix_acl_access payload: a version header followed by
// {tag, perm, id} entries, native byte order — the layout the kernel writes.
func acl(entries ...[3]uint32) []byte {
	b := make([]byte, aclHeaderSize, aclHeaderSize+len(entries)*aclEntrySize)
	native.PutUint32(b, aclEAVersion)
	for _, e := range entries {
		var ent [aclEntrySize]byte
		native.PutUint16(ent[0:], uint16(e[0]))
		native.PutUint16(ent[2:], uint16(e[1]))
		native.PutUint32(ent[4:], e[2])
		b = append(b, ent[:]...)
	}
	return b
}

func TestParseACLGrant(t *testing.T) {
	const uid = 1000
	rw := uint32(aclPermRead | aclPermWrite)

	cases := []struct {
		name string
		buf  []byte
		want aclVerdict
	}{
		{"setfacl -m u:1000:rw", acl([3]uint32{aclTagUser, rw, uid}), aclAllows},
		{"entry for someone else", acl([3]uint32{aclTagUser, rw, 1001}), aclSilent},
		{"read-only entry", acl([3]uint32{aclTagUser, aclPermRead, uid}), aclSilent},
		// The mask is not advisory: the kernel intersects it with every named
		// entry, so a grant the mask strips is not a grant.
		{"mask strips write", acl(
			[3]uint32{aclTagUser, rw, uid},
			[3]uint32{aclTagMask, aclPermRead, 0},
		), aclSilent},
		{"mask permits", acl(
			[3]uint32{aclTagUser, rw, uid},
			[3]uint32{aclTagMask, rw, 0},
		), aclAllows},
		// Anything we cannot read is the absence of an answer, never a denial.
		{"empty", nil, aclUnknown},
		{"truncated header", []byte{0x02, 0x00}, aclUnknown},
		{"wrong version", append([]byte{0x09, 0x00, 0x00, 0x00},
			make([]byte, aclEntrySize)...), aclUnknown},
		{"header only", acl(), aclSilent},
		// A trailing partial entry must be ignored, not read out of bounds.
		{"trailing garbage", append(acl([3]uint32{aclTagUser, rw, uid}), 0x01, 0x02), aclAllows},
	}
	for _, c := range cases {
		if got := parseACLGrant(c.buf, uid); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
