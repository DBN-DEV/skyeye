package tsdb

import "fmt"

type Config struct {
	Backend string `json:"backend"`

	// InfluxDB options.
	InfluxDBServerURL string `json:"influxdb_server_url,omitempty"`
	InfluxDBToken     string `json:"influxdb_token,omitempty"`
	InfluxDBOrg       string `json:"influxdb_org,omitempty"`
	InfluxDBBucket    string `json:"influxdb_bucket,omitempty"`
}

func NewWriterFromConfig(cfg Config) (Writer, error) {
	switch cfg.Backend {
	case "influxdb":
		if cfg.InfluxDBServerURL == "" {
			return nil, fmt.Errorf("tsdb: influxdb server URL is required")
		}
		return NewInfluxDBWriter(cfg.InfluxDBServerURL, cfg.InfluxDBToken, cfg.InfluxDBOrg, cfg.InfluxDBBucket), nil
	case "log":
		return NewLogWriter(), nil
	default:
		return nil, fmt.Errorf("tsdb: unknown backend %q", cfg.Backend)
	}
}
