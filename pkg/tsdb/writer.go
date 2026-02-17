package tsdb

import "context"

type Writer interface {
	Write(ctx context.Context, points ...Point) error
	Close() error
}
