module github.com/YaaMe/cidr-ipspan/bench

go 1.24.0

toolchain go1.26.8

require (
	github.com/YaaMe/cidr-ipspan v0.0.0
	github.com/gaissmai/bart v0.29.0
	github.com/yl2chen/cidranger v1.0.2
	go4.org/netipx v0.0.0-20260823151212-3075585bcbeb
)

replace github.com/YaaMe/cidr-ipspan => ..
