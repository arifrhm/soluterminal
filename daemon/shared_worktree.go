package main

import (
	"context"

	"fmt"

	"os"

	"path/filepath"

	"strings"
)

// dirtySummary renders the blocking files for a one-line status.
func dirtySummary(files []string) string {
	const show = 3
	if len(files) == 0 {
		return ""
	}
	if len(files) <= show {
		return strings.Join(files, ", ")
	}
	return fmt.Sprintf("%s +%d more", strings.Join(files[:show], ", "), len(files)-show)
}

// dirSizeBytesContext is the request-path variant of a full directory-size walk. It
// returns a lower-bound estimate when cancellation or an unreadable entry
// prevents a complete walk.
func dirSizeBytesContext(ctx context.Context, path string) (total int64, complete bool) {
	complete = true
	err := filepath.WalkDir(path, func(_ string, d os.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			complete = false
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			complete = false
			return nil
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		complete = false
	}
	return total, complete
}
