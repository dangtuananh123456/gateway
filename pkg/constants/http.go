package constants

const (
	ContentTypeJSON                     = "application/json"
	CreateSMContextMaxBodyBytes   int64 = 1 << 20
	CollectorMaxResponseBodyBytes int64 = 64 << 10
	ProxyCopyBufferBytes                = 32 << 10
	CreateSMContextPath                 = "/nsmf-pdusession/v1/sm-contexts"
	HealthPath                          = "/health"
	MetricsPath                         = "/metrics"
)
