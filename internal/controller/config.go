package controller

import (
	"fmt"
	"path"

	v1 "github.com/clickhouse-operator/api/v1alpha1"
)

type LoggerConfig struct {
	Console    bool   `yaml:"console"`
	Level      string `yaml:"level"`
	Formatting struct {
		Type string `yaml:"type"`
	} `yaml:"formatting,omitempty"`
	// File logging settings
	Log      string `yaml:"log,omitempty"`
	ErrorLog string `yaml:"errorlog,omitempty"`
	Size     string `yaml:"size,omitempty"`
	Count    int64  `yaml:"count,omitempty"`
}

func GenerateLoggerConfig(spec v1.LoggerConfig, basePath string, service string) LoggerConfig {
	config := LoggerConfig{
		Console: true,
		Level:   spec.Level,
		Size:    spec.Size,
		Count:   spec.Count,
	}

	if spec.JSONLogs {
		config.Formatting.Type = "json"
	}

	if spec.LogToFile {
		config.Log = path.Join(basePath, fmt.Sprintf("%s.log", service))
		config.ErrorLog = path.Join(basePath, fmt.Sprintf("%s.err.log", service))
	}

	return config
}

type PrometheusConfig struct {
	Endpoint            string `yaml:"endpoint"`
	Port                uint16 `yaml:"port"`
	Metrics             bool   `yaml:"metrics"`
	Events              bool   `yaml:"events"`
	AsynchronousMetrics bool   `yaml:"asynchronous_metrics"`
}

func DefaultPrometheusConfig(port uint16) PrometheusConfig {
	return PrometheusConfig{
		Endpoint:            "/metrics",
		Port:                port,
		Metrics:             true,
		Events:              true,
		AsynchronousMetrics: true,
	}
}

type OpenSSLParams struct {
	CertificateFile     string `yaml:"certificateFile"`
	PrivateKeyFile      string `yaml:"privateKeyFile"`
	CAConfig            string `yaml:"caConfig"`
	VerificationMode    string `yaml:"verificationMode"`
	DisableProtocols    string `yaml:"disableProtocols"`
	PreferServerCiphers bool   `yaml:"preferServerCiphers"`
}

type OpenSSLConfig struct {
	Server OpenSSLParams `yaml:"server,omitempty"`
	Client OpenSSLParams `yaml:"client,omitempty"`
}
