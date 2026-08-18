package constants

const (
	ContentTypeJSON                     = "application/json"
	CreateSMContextMaxBodyBytes   int64 = 1 << 20
	CollectorMaxResponseBodyBytes int64 = 64 << 10
	ClientMaxRequestBodyBytes     int64 = 2 << 20
	ClientMaxResponseBodyBytes    int64 = 2 << 20
	// Proxy responses in this project are small JSON documents. A 4 KiB
	// pooled buffer avoids retaining 32 KiB for every concurrent HTTP/2 stream.
	ProxyCopyBufferBytes        = 4 << 10
	GatewayMaxConcurrentStreams = 2048
	PDUMaxConcurrentStreams     = 4096
	// The h2c transport multiplexes requests per TCP connection. These limits
	// allow additional connections when all streams are occupied while keeping
	// the long-lived per-PDU pool bounded.
	UpstreamMaxIdleConnections         = 1024
	UpstreamMaxIdleConnectionsPerHost  = 32
	UpstreamMaxConnectionsPerHost      = 32
	DiscoveryMaxIdleConnections        = 64
	DiscoveryMaxIdleConnectionsPerHost = 2
	DiscoveryMaxConnectionsPerHost     = 2
	DiscoveryHealthFailureThreshold    = 3
	CreateSMContextPath                = "/nsmf-pdusession/v1/sm-contexts"
	HealthPath                         = "/health"
	MetricsPath                        = "/metrics"
	GatewayBackendsPath                = "/gateway/backends"
	GatewayStatsPath                   = "/gateway/stats"
	ClientPerformanceRoundRobinPath    = "/api/performance/round-robin"
	ClientPerformanceWeightedPath      = "/api/performance/weighted"
	ClientPerformanceLoadPath          = "/api/performance/load"
)
