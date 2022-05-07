package gcp

import (
	"context"
	"encoding/base64"
	"github.com/viant/scy/auth/gcp"
	"strings"
)

//JwtClaim represents IDJWT visitor
type JwtClaim struct{}

func (j *JwtClaim) Value(ctx context.Context, raw string) (interface{}, error) {
	if last := strings.LastIndexByte(raw, ' '); last != -1 {
		raw = raw[last+1:]
	}

	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, err
	}

	info, err := gcp.TokenInfo(ctx, string(decoded), false)
	if err != nil {
		return nil, err
	}

	return info, nil
}
