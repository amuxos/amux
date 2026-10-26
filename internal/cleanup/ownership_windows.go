package cleanup

import "os"

func preserveOwnership(_ *os.File, _ os.FileInfo) error { return nil }
