//go:build unix

package sysstats

import "syscall"

// statfs is the production Statfs: real statfs(2). Used/total mirror
// df(1)'s accounting: used counts blocks unavailable to anyone, total
// excludes the root-reserved slice so used/total ≈ df's Use%.
func statfs(path string) (used, total uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bsize := uint64(st.Bsize) //nolint:gosec // block size is never negative (int64 on Linux, uint32 on Darwin)
	used = (st.Blocks - st.Bfree) * bsize
	total = used + st.Bavail*bsize
	return used, total, nil
}
