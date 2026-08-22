module github.com/IceCodeNew/maniud/containerconfig/runtimeargv

go 1.26.0

toolchain go1.26.6

require (
	github.com/IceCodeNew/maniud/argv v0.1.0
	github.com/IceCodeNew/maniud/containerconfig v0.1.0
	github.com/IceCodeNew/maniud/imageref v0.1.0
)

require (
	github.com/distribution/reference v0.6.0 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
)

replace github.com/IceCodeNew/maniud/containerconfig => ..

replace github.com/IceCodeNew/maniud/argv => ../../argv

replace github.com/IceCodeNew/maniud/imageref => ../../imageref
