package registry

import (
	"context"
	"fmt"
	"strings"
)

type AsStrings struct {
}

func (s *AsStrings) Value(ctx context.Context, raw interface{}, options ...interface{}) (interface{}, error) {
	expectedRaw, ok := raw.(string)
	if !ok {
		return nil, fmt.Errorf("expected to get string but got %T", raw)
	}

	return strings.Split(expectedRaw, ","), nil
}
