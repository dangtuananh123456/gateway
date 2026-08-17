package constants

const (
	ContentTypeJSON                         = "application/json"
	CreateSMContextMaxBodyBytes       int64 = 1 << 20
	CollectorMaxResponseBodyBytes     int64 = 64 << 10
	ClientMaxRequestBodyBytes         int64 = 2 << 20
	ClientMaxResponseBodyBytes        int64 = 2 << 20
	ProxyCopyBufferBytes                    = 32 << 10
	UpstreamMaxIdleConnections              = 4096
	UpstreamMaxIdleConnectionsPerHost       = 256
	CreateSMContextPath                     = "/nsmf-pdusession/v1/sm-contexts"
	HealthPath                              = "/health"
	MetricsPath                             = "/metrics"
	GatewayBackendsPath                     = "/gateway/backends"
	GatewayStatsPath                        = "/gateway/stats"
	ClientPerformanceRoundRobinPath         = "/api/performance/round-robin"
	ClientPerformanceWeightedPath           = "/api/performance/weighted"
	ClientPerformanceLoadPath               = "/api/performance/load"
)
