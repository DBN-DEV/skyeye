package tsdb

import "time"

type Point struct {
	Measurement string
	Tags        map[string]string
	Fields      map[string]any
	Time        time.Time
}
