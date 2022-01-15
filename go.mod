module github.com/ncabatoff/yurt

replace github.com/hashicorp/vault/api => ../../hc/vault/api

require (
	github.com/docker/docker v0.7.3-0.20200123194546-ac058c1629dc
	github.com/docker/go-connections v0.4.0
	github.com/google/go-cmp v0.5.6
	github.com/hashicorp/consul/api v1.3.0
	github.com/hashicorp/go-getter v1.5.10
	github.com/hashicorp/go-multierror v1.1.1
	github.com/hashicorp/go-sockaddr v1.0.2
	github.com/hashicorp/go-uuid v1.0.2
	github.com/hashicorp/nomad/api v0.0.0-20200124004857-fea44b0d8e20
	github.com/hashicorp/vault/api v1.3.1
	github.com/hashicorp/vault/sdk v0.3.0
	github.com/pkg/errors v0.9.1
	github.com/prometheus/client_golang v1.4.1
	github.com/prometheus/common v0.9.1
	github.com/skratchdot/open-golang v0.0.0-20200116055534-eef842397966
	go.uber.org/atomic v1.9.0
	golang.org/x/sync v0.0.0-20210220032951-036812b2e83c
	gopkg.in/yaml.v2 v2.2.8
)

require (
	cloud.google.com/go v0.99.0 // indirect
	cloud.google.com/go/storage v1.18.2 // indirect
	github.com/Azure/go-ansiterm v0.0.0-20170929234023-d6e3b3328b78 // indirect
	github.com/Microsoft/go-winio v0.4.15-0.20190919025122-fc70bd9a86b5 // indirect
	github.com/Microsoft/hcsshim v0.8.9 // indirect
	github.com/armon/go-metrics v0.3.9 // indirect
	github.com/armon/go-radix v1.0.0 // indirect
	github.com/aws/aws-sdk-go v1.42.25 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/bgentry/go-netrc v0.0.0-20140422174119-9fd32a8b3d3d // indirect
	github.com/cenkalti/backoff/v3 v3.0.0 // indirect
	github.com/census-instrumentation/opencensus-proto v0.3.0 // indirect
	github.com/cespare/xxhash/v2 v2.1.2 // indirect
	github.com/cncf/udpa/go v0.0.0-20210930031921-04548b0d99d4 // indirect
	github.com/cncf/xds/go v0.0.0-20211216145620-d92e9ce0af51 // indirect
	github.com/containerd/containerd v1.3.2 // indirect
	github.com/containerd/continuity v0.0.0-20200413184840-d3ef23f19fbb // indirect
	github.com/docker/distribution v2.7.1+incompatible // indirect
	github.com/docker/go-units v0.4.0 // indirect
	github.com/envoyproxy/go-control-plane v0.10.1 // indirect
	github.com/envoyproxy/protoc-gen-validate v0.6.2 // indirect
	github.com/fatih/color v1.7.0 // indirect
	github.com/gogo/protobuf v1.3.1 // indirect
	github.com/golang/groupcache v0.0.0-20210331224755-41bb18bfe9da // indirect
	github.com/golang/protobuf v1.5.2 // indirect
	github.com/golang/snappy v0.0.4 // indirect
	github.com/googleapis/gax-go/v2 v2.1.1 // indirect
	github.com/gorhill/cronexpr v0.0.0-20180427100037-88b0669f7d75 // indirect
	github.com/gorilla/mux v1.7.3 // indirect
	github.com/gorilla/websocket v1.4.1 // indirect
	github.com/hashicorp/errwrap v1.1.0 // indirect
	github.com/hashicorp/go-cleanhttp v0.5.2 // indirect
	github.com/hashicorp/go-hclog v0.16.2 // indirect
	github.com/hashicorp/go-immutable-radix v1.3.1 // indirect
	github.com/hashicorp/go-plugin v1.4.3 // indirect
	github.com/hashicorp/go-retryablehttp v0.6.6 // indirect
	github.com/hashicorp/go-rootcerts v1.0.2 // indirect
	github.com/hashicorp/go-safetemp v1.0.0 // indirect
	github.com/hashicorp/go-secure-stdlib/mlock v0.1.1 // indirect
	github.com/hashicorp/go-secure-stdlib/parseutil v0.1.1 // indirect
	github.com/hashicorp/go-secure-stdlib/strutil v0.1.1 // indirect
	github.com/hashicorp/go-version v1.3.0 // indirect
	github.com/hashicorp/golang-lru v0.5.4 // indirect
	github.com/hashicorp/hcl v1.0.0 // indirect
	github.com/hashicorp/serf v0.8.2 // indirect
	github.com/hashicorp/yamux v0.0.0-20181012175058-2f1d1f20f75d // indirect
	github.com/jmespath/go-jmespath v0.4.0 // indirect
	github.com/json-iterator/go v1.1.10 // indirect
	github.com/klauspost/compress v1.13.6 // indirect
	github.com/konsorten/go-windows-terminal-sequences v1.0.1 // indirect
	github.com/mattn/go-colorable v0.1.6 // indirect
	github.com/mattn/go-isatty v0.0.12 // indirect
	github.com/matttproud/golang_protobuf_extensions v1.0.1 // indirect
	github.com/mitchellh/copystructure v1.0.0 // indirect
	github.com/mitchellh/go-homedir v1.1.0 // indirect
	github.com/mitchellh/go-testing-interface v1.14.1 // indirect
	github.com/mitchellh/mapstructure v1.4.2 // indirect
	github.com/mitchellh/reflectwalk v1.0.0 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.1 // indirect
	github.com/morikuni/aec v1.0.0 // indirect
	github.com/mwitkow/go-conntrack v0.0.0-20161129095857-cc309e4a2223 // indirect
	github.com/oklog/run v1.0.0 // indirect
	github.com/opencontainers/go-digest v1.0.0-rc1 // indirect
	github.com/opencontainers/image-spec v1.0.1 // indirect
	github.com/opencontainers/runc v0.1.1 // indirect
	github.com/pierrec/lz4 v2.5.2+incompatible // indirect
	github.com/prometheus/client_model v0.2.0 // indirect
	github.com/prometheus/procfs v0.0.8 // indirect
	github.com/ryanuber/go-glob v1.0.0 // indirect
	github.com/sirupsen/logrus v1.4.2 // indirect
	github.com/ulikunitz/xz v0.5.10 // indirect
	go.opencensus.io v0.23.0 // indirect
	golang.org/x/crypto v0.0.0-20210711020723-a769d52b0f97 // indirect
	golang.org/x/net v0.0.0-20211216030914-fe4d6282115f // indirect
	golang.org/x/oauth2 v0.0.0-20211104180415-d3ed0bb246c8 // indirect
	golang.org/x/sys v0.0.0-20211216021012-1d35b9e2eb4e // indirect
	golang.org/x/text v0.3.7 // indirect
	golang.org/x/time v0.0.0-20200416051211-89c76fbcd5d1 // indirect
	golang.org/x/xerrors v0.0.0-20200804184101-5ec99f83aff1 // indirect
	google.golang.org/api v0.63.0 // indirect
	google.golang.org/appengine v1.6.7 // indirect
	google.golang.org/genproto v0.0.0-20211223182754-3ac035c7e7cb // indirect
	google.golang.org/grpc v1.43.0 // indirect
	google.golang.org/protobuf v1.27.1 // indirect
	gopkg.in/square/go-jose.v2 v2.5.1 // indirect
)

go 1.17
