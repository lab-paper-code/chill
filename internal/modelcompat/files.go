package modelcompat

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// ValidateModelFileNames checks the names of one artifact's model files
// against what the firstfloor runner can load. Sessions are keyed by file
// stem, so stems are unique; an RKNN artifact is exactly one .rknn file.
// Path syntax stays with each caller.
func ValidateModelFileNames(files []string, rknn bool) error {
	if rknn && (len(files) != 1 || path.Ext(files[0]) != ".rknn") {
		return errors.New("RKNN artifact requires exactly one .rknn model file")
	}
	stems := make(map[string]struct{}, len(files))
	for _, file := range files {
		stem := ModelFileStem(path.Base(file))
		if _, duplicate := stems[stem]; duplicate {
			return fmt.Errorf("model files share session name %q", stem)
		}
		stems[stem] = struct{}{}
	}
	return nil
}

// ModelFileStem is Python's PurePath(name).stem: the name without its last
// suffix, where a leading or trailing dot does not start a suffix.
func ModelFileStem(name string) string {
	if i := strings.LastIndex(name, "."); i > 0 && i < len(name)-1 {
		return name[:i]
	}
	return name
}
