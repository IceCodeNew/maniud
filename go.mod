module github.com/IceCodeNew/maniud

go 1.26.0

toolchain go1.26.6

require (
	github.com/IceCodeNew/maniud/containerconfig v0.1.0
	github.com/IceCodeNew/maniud/containerconfig/compose v0.1.0
	github.com/IceCodeNew/maniud/containerconfig/runtimeargv v0.1.0
	github.com/IceCodeNew/maniud/imageref v0.1.0
	github.com/compose-spec/compose-go/v2 v2.14.0
	github.com/opencontainers/go-digest v1.0.0
	github.com/opencontainers/image-spec v1.1.1
	go.yaml.in/yaml/v4 v4.0.0-rc.4
	golang.org/x/sys v0.47.0
	modernc.org/sqlite v1.56.0
	oras.land/oras-go/v2 v2.6.2
)

require (
	github.com/distribution/reference v0.6.0 // indirect
	github.com/docker/go-connections v0.4.0 // indirect
	github.com/docker/go-units v0.5.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/go-viper/mapstructure/v2 v2.4.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/mattn/go-shellwords v1.0.12 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.1 // indirect
	github.com/sirupsen/logrus v1.9.1 // indirect
	github.com/xhit/go-str2duration/v2 v2.1.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/text v0.39.0 // indirect
	modernc.org/libc v1.74.4 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)

replace (
	github.com/IceCodeNew/maniud/containerconfig => ./containerconfig
	github.com/IceCodeNew/maniud/containerconfig/compose => ./containerconfig/compose
	github.com/IceCodeNew/maniud/containerconfig/runtimeargv => ./containerconfig/runtimeargv
	github.com/IceCodeNew/maniud/imageref => ./imageref
)
