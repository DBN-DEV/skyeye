package tsdb

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/DBN-DEV/skyeye/pkg/log"
)

type LogWriter struct{}

func NewLogWriter() *LogWriter {
	return &LogWriter{}
}

func (w *LogWriter) Write(_ context.Context, points ...Point) error {
	for _, p := range points {
		log.Info("tsdb point",
			zap.String("measurement", p.Measurement),
			zap.String("tags", fmt.Sprint(p.Tags)),
			zap.String("fields", fmt.Sprint(p.Fields)),
			zap.Time("time", p.Time),
		)
	}
	return nil
}

func (w *LogWriter) Close() error {
	return nil
}
