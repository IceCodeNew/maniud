module github.com/IceCodeNew/maniud/containerconfig/docker

go 1.27.0

require (
	github.com/IceCodeNew/maniud/containerconfig v0.2.0
	github.com/moby/moby/api v1.55.0
)

require (
	github.com/docker/go-units v0.5.0 // indirect
	github.com/moby/docker-image-spec v1.3.1 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/opencontainers/image-spec v1.1.1 // indirect
)

replace github.com/IceCodeNew/maniud/containerconfig => ..
