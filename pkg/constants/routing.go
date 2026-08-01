package constants

// RoutingMode identifies the routing algorithm selected at startup.
type RoutingMode string

const (
	RoutingRoundRobin RoutingMode = "round_robin"
	RoutingWeighted   RoutingMode = "weighted"
	RoutingLoad       RoutingMode = "load"
)
