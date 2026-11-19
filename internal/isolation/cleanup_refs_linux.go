//go:build linux

package isolation

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/amuxos/amux/internal/cleanup"
	"golang.org/x/sys/unix"
)

const (
	// Keep inline requests comfortably within the Linux Unix packet limit.
	// Small requests remain compatible with helpers that expect inline JSON.
	maxInlineCleanupReferencesBytes = 64 << 10
	maxCleanupReferencesBytes       = 64 << 20
	cleanupReferenceSeals           = unix.F_SEAL_WRITE | unix.F_SEAL_SHRINK | unix.F_SEAL_GROW | unix.F_SEAL_SEAL
)

func sealCleanupReferences(data []byte) (*os.File, error) {
	if len(data) > maxCleanupReferencesBytes {
		return nil, errors.New("cleanup reference snapshot is too large")
	}
	fd, err := unix.MemfdCreate("amux-cleanup-references", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, fmt.Errorf("create cleanup reference file: %w", err)
	}
	file := os.NewFile(uintptr(fd), "cleanup-references")
	if _, err := file.Write(data); err != nil {
		file.Close()
		return nil, err
	}
	if _, err := unix.FcntlInt(file.Fd(), unix.F_ADD_SEALS, cleanupReferenceSeals); err != nil {
		file.Close()
		return nil, fmt.Errorf("seal cleanup reference file: %w", err)
	}
	return file, nil
}

func readCleanupReferences(req *DailyCleanupRequest, files []*os.File) (cleanup.References, error) {
	if req != nil && req.References != nil && len(files) == 0 {
		return *req.References, nil
	}
	if req == nil || req.References != nil || len(files) != 1 {
		return cleanup.References{}, errors.New("exactly one cleanup reference snapshot is required")
	}
	file := files[0]
	seals, err := unix.FcntlInt(file.Fd(), unix.F_GET_SEALS, 0)
	if err != nil || seals&cleanupReferenceSeals != cleanupReferenceSeals {
		return cleanup.References{}, errors.New("cleanup reference file must be sealed")
	}
	info, err := file.Stat()
	if err != nil {
		return cleanup.References{}, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxCleanupReferencesBytes {
		return cleanup.References{}, errors.New("invalid cleanup reference file size")
	}
	data := make([]byte, int(info.Size()))
	if _, err := file.ReadAt(data, 0); err != nil {
		return cleanup.References{}, err
	}
	var refs *cleanup.References
	if err := json.Unmarshal(data, &refs); err != nil {
		return cleanup.References{}, err
	}
	if refs == nil {
		return cleanup.References{}, errors.New("cleanup references are required")
	}
	return *refs, nil
}
