module github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikereceiver

go 1.26.0

require (
	github.com/crowdstrike/gofalcon v0.18.0
	github.com/go-openapi/strfmt v0.22.2
	github.com/open-telemetry/opentelemetry-collector-contrib/extension/storage v0.166.0
	github.com/stretchr/testify v1.12.1
	go.opentelemetry.io/collector/component v1.72.0
	go.opentelemetry.io/collector/component/componenttest v0.166.0
	go.opentelemetry.io/collector/config/configopaque v1.72.0
	go.opentelemetry.io/collector/config/configtls v1.72.0
	go.opentelemetry.io/collector/confmap v1.72.0
	go.opentelemetry.io/collector/consumer v1.72.0
	go.opentelemetry.io/collector/consumer/consumertest v0.166.0
	go.opentelemetry.io/collector/extension/xextension v0.166.0
	go.opentelemetry.io/collector/pdata v1.72.0
	go.opentelemetry.io/collector/receiver v1.72.0
	go.opentelemetry.io/collector/receiver/receiverhelper v0.166.0
	go.opentelemetry.io/collector/receiver/receivertest v0.166.0
	go.uber.org/goleak v1.3.0
	go.uber.org/zap v1.28.0
	golang.org/x/oauth2 v0.36.0
)

require (
	github.com/asaskevich/govalidator v0.0.0-20230301143203-a9d515a09cc2 // indirect
	github.com/blang/semver/v4 v4.0.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/foxboron/go-tpm-keyfiles v0.0.0-20260902202739-8c9c2d1005f4 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-openapi/analysis v0.22.2 // indirect
	github.com/go-openapi/errors v0.22.0 // indirect
	github.com/go-openapi/jsonpointer v0.20.2 // indirect
	github.com/go-openapi/jsonreference v0.20.4 // indirect
	github.com/go-openapi/loads v0.21.5 // indirect
	github.com/go-openapi/runtime v0.27.1 // indirect
	github.com/go-openapi/spec v0.20.14 // indirect
	github.com/go-openapi/swag v0.22.9 // indirect
	github.com/go-openapi/validate v0.23.1 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/gobwas/glob v0.2.3 // indirect
	github.com/google/go-tpm v0.9.9-0.20260124013517-8f8f42cba0de // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/hashicorp/go-version v1.9.0 // indirect
	github.com/josharian/intern v1.0.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/knadh/koanf/maps v0.1.3 // indirect
	github.com/knadh/koanf/providers/confmap v1.0.1 // indirect
	github.com/knadh/koanf/v2 v2.4.1 // indirect
	github.com/mailru/easyjson v0.7.7 // indirect
	github.com/mitchellh/copystructure v1.2.0 // indirect
	github.com/mitchellh/mapstructure v1.5.0 // indirect
	github.com/mitchellh/reflectwalk v1.0.2 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.3-0.20250322232337-35a7c28c31ee // indirect
	github.com/oklog/ulid v1.3.1 // indirect
	github.com/opentracing/opentracing-go v1.2.0 // indirect
	github.com/sirupsen/logrus v1.9.3 // indirect
	go.mongodb.org/mongo-driver v1.14.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/collector/consumer/consumererror v0.166.0 // indirect
	go.opentelemetry.io/collector/consumer/xconsumer v0.166.0 // indirect
	go.opentelemetry.io/collector/extension v1.72.0 // indirect
	go.opentelemetry.io/collector/featuregate v1.72.0 // indirect
	go.opentelemetry.io/collector/internal/componentalias v0.166.0 // indirect
	go.opentelemetry.io/collector/pdata/pprofile v0.166.0 // indirect
	go.opentelemetry.io/collector/pipeline v1.72.0 // indirect
	go.opentelemetry.io/collector/pipeline/xpipeline v0.166.0 // indirect
	go.opentelemetry.io/collector/receiver/xreceiver v0.166.0 // indirect
	go.opentelemetry.io/otel v1.47.0 // indirect
	go.opentelemetry.io/otel/log v1.47.0 // indirect
	go.opentelemetry.io/otel/metric v1.47.0 // indirect
	go.opentelemetry.io/otel/sdk v1.47.0 // indirect
	go.opentelemetry.io/otel/sdk/metric v1.47.0 // indirect
	go.opentelemetry.io/otel/trace v1.47.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.yaml.in/yaml/v3 v3.0.6 // indirect
	golang.org/x/crypto v0.52.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260817212433-ac3dfec99bb1 // indirect
	google.golang.org/grpc v1.85.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/open-telemetry/opentelemetry-collector-contrib/extension/storage => ../../extension/storage
