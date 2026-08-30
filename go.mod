module github.com/IceCodeNew/maniud

go 1.27.0

require (
	charm.land/bubbletea/v2 v2.0.9
	github.com/IceCodeNew/maniud/argv v0.2.0
	github.com/IceCodeNew/maniud/containerconfig v0.2.0
	github.com/IceCodeNew/maniud/containerconfig/compose v0.2.0
	github.com/IceCodeNew/maniud/containerconfig/containerd v0.2.0
	github.com/IceCodeNew/maniud/containerconfig/docker v0.2.0
	github.com/IceCodeNew/maniud/containerconfig/nerdctl v0.2.0
	github.com/IceCodeNew/maniud/containerconfig/podman v0.2.0
	github.com/IceCodeNew/maniud/containerconfig/runtimeargv v0.2.0
	github.com/IceCodeNew/maniud/imageref v0.2.0
	github.com/alecthomas/kong v1.16.1
	github.com/charmbracelet/x/ansi v0.11.7
	github.com/charmbracelet/x/term v0.2.2
	github.com/compose-spec/compose-go/v2 v2.14.0
	github.com/containerd/containerd/api v1.11.1
	github.com/containerd/containerd/v2 v2.3.4
	github.com/containerd/continuity v0.5.0
	github.com/containerd/log v0.1.0
	github.com/containernetworking/cni v1.3.0
	github.com/google/go-cmp v0.7.0
	github.com/google/go-containerregistry v0.22.0
	github.com/moby/moby/api v1.55.0
	github.com/moby/sys/user v0.4.1
	github.com/nikoksr/notify v1.5.0
	github.com/opencontainers/go-digest v1.0.0
	github.com/opencontainers/image-spec v1.1.1
	github.com/opencontainers/runtime-spec v1.3.0
	github.com/sirupsen/logrus v1.10.2
	go.yaml.in/yaml/v4 v4.0.0-rc.6
	golang.org/x/crypto v0.55.0
	golang.org/x/mod v0.40.0
	golang.org/x/sys v0.47.0
	golang.org/x/text v0.41.0
	google.golang.org/grpc v1.83.2
	google.golang.org/protobuf v1.36.12
	modernc.org/sqlite v1.57.0
	oras.land/oras-go/v2 v2.6.2
)

require (
	github.com/Microsoft/go-winio v0.6.3-0.20251027160822-ad3df93bed29 // indirect
	github.com/Microsoft/hcsshim v0.15.0-rc.1 // indirect
	github.com/charmbracelet/colorprofile v0.4.3 // indirect
	github.com/charmbracelet/ultraviolet v0.0.0-20260703014108-f5a850f9c2b7 // indirect
	github.com/charmbracelet/x/termios v0.1.1 // indirect
	github.com/charmbracelet/x/windows v0.2.2 // indirect
	github.com/clipperhouse/displaywidth v0.11.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/containerd/cgroups/v3 v3.1.3 // indirect
	github.com/containerd/errdefs v1.0.0 // indirect
	github.com/containerd/errdefs/pkg v0.3.0 // indirect
	github.com/containerd/ttrpc v1.2.8 // indirect
	github.com/containerd/typeurl/v2 v2.2.3 // indirect
	github.com/distribution/reference v0.6.0 // indirect
	github.com/docker/go-connections v0.7.0 // indirect
	github.com/docker/go-units v0.5.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/go-viper/mapstructure/v2 v2.4.0 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang/groupcache v0.0.0-20241129210726-2c02b8208cf8 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/klauspost/compress v1.19.2 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/mattn/go-runewidth v0.0.23 // indirect
	github.com/mattn/go-shellwords v1.0.12 // indirect
	github.com/moby/docker-image-spec v1.3.1 // indirect
	github.com/moby/sys/mountinfo v0.7.2 // indirect
	github.com/moby/sys/sequential v0.6.0 // indirect
	github.com/moby/sys/userns v0.1.0 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.1 // indirect
	github.com/xhit/go-str2duration/v2 v2.1.0 // indirect
	github.com/xo/terminfo v0.0.0-20220910002029-abceb7e1c41e // indirect
	go.opencensus.io v0.24.0 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260807164820-c8921c73eeea // indirect
	modernc.org/libc v1.74.4 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)

replace (
	github.com/IceCodeNew/maniud/argv => ./argv
	github.com/IceCodeNew/maniud/containerconfig => ./containerconfig
	github.com/IceCodeNew/maniud/containerconfig/compose => ./containerconfig/compose
	github.com/IceCodeNew/maniud/containerconfig/containerd => ./containerconfig/containerd
	github.com/IceCodeNew/maniud/containerconfig/docker => ./containerconfig/docker
	github.com/IceCodeNew/maniud/containerconfig/nerdctl => ./containerconfig/nerdctl
	github.com/IceCodeNew/maniud/containerconfig/podman => ./containerconfig/podman
	github.com/IceCodeNew/maniud/containerconfig/runtimeargv => ./containerconfig/runtimeargv
	github.com/IceCodeNew/maniud/imageref => ./imageref
	github.com/nikoksr/notify => github.com/IceCodeNew/notify v0.0.0-20260825033926-0a827094afac
)
