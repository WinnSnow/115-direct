package pan115

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
)

// Share responses mix string and numeric IDs. Preserve the integer's exact
// digits without converting through float64 or imposing an int64 size limit.
type shareID string

var shareIDNumberPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)

func (id *shareID) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("null")) {
		*id = ""
		return nil
	}
	if len(data) > 0 && data[0] == '"' {
		var value string
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		*id = shareID(value)
		return nil
	}
	if !shareIDNumberPattern.Match(data) {
		return fmt.Errorf("share ID must be a string or non-negative integer")
	}
	*id = shareID(data)
	return nil
}
