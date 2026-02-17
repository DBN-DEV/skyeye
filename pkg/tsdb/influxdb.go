package tsdb

import (
	"context"

	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	"github.com/influxdata/influxdb-client-go/v2/api"
)

type InfluxDBWriter struct {
	client   influxdb2.Client
	writeAPI api.WriteAPI
}

func NewInfluxDBWriter(serverURL, token, org, bucket string) *InfluxDBWriter {
	client := influxdb2.NewClient(serverURL, token)
	writeAPI := client.WriteAPI(org, bucket)
	return &InfluxDBWriter{
		client:   client,
		writeAPI: writeAPI,
	}
}

func (w *InfluxDBWriter) Write(_ context.Context, points ...Point) error {
	for _, p := range points {
		pt := influxdb2.NewPoint(p.Measurement, p.Tags, p.Fields, p.Time)
		w.writeAPI.WritePoint(pt)
	}
	return nil
}

func (w *InfluxDBWriter) Close() error {
	w.writeAPI.Flush()
	w.client.Close()
	return nil
}
