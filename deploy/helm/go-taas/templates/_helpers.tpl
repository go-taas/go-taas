{{/* Helpers shared by every template in the chart. */}}

{{/* ---- names and labels ------------------------------------------------ */}}

{{/* Chart name, overridable through nameOverride. */}}
{{- define "go-taas.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Fully qualified release name, used as the prefix of every resource. */}}
{{- define "go-taas.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* Chart name and version, as used by the helm.sh/chart label. */}}
{{- define "go-taas.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Labels applied to every resource. */}}
{{- define "go-taas.labels" -}}
helm.sh/chart: {{ include "go-taas.chart" . }}
{{ include "go-taas.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: go-taas
{{- end -}}

{{/* Selector labels shared by a component's workload and its Service. */}}
{{- define "go-taas.selectorLabels" -}}
app.kubernetes.io/name: {{ include "go-taas.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/* ---- resource names -------------------------------------------------- */}}

{{- define "go-taas.server.name" -}}
{{- printf "%s-server" (include "go-taas.fullname" .) -}}
{{- end -}}

{{- define "go-taas.controller.name" -}}
{{- printf "%s-controller" (include "go-taas.fullname" .) -}}
{{- end -}}

{{- define "go-taas.postgresql.name" -}}
{{- printf "%s-postgresql" (include "go-taas.fullname" .) -}}
{{- end -}}

{{- define "go-taas.redis.name" -}}
{{- printf "%s-redis" (include "go-taas.fullname" .) -}}
{{- end -}}

{{- define "go-taas.rabbitmq.name" -}}
{{- printf "%s-rabbitmq" (include "go-taas.fullname" .) -}}
{{- end -}}

{{- define "go-taas.keycloak.name" -}}
{{- printf "%s-keycloak" (include "go-taas.fullname" .) -}}
{{- end -}}

{{- define "go-taas.configOverrides.name" -}}
{{- printf "%s-config-overrides" (include "go-taas.fullname" .) -}}
{{- end -}}

{{/* ---- service accounts ------------------------------------------------ */}}

{{- define "go-taas.server.serviceAccountName" -}}
{{- if .Values.taasServer.serviceAccount.create -}}
{{- default (include "go-taas.server.name" .) .Values.taasServer.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.taasServer.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "go-taas.controller.serviceAccountName" -}}
{{- if .Values.controller.serviceAccount.create -}}
{{- default (include "go-taas.controller.name" .) .Values.controller.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.controller.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/* ---- images ---------------------------------------------------------- */}}

{{/* Control-plane image reference. Call with (dict "root" $ "name" "taas-server" "image" .Values.taasServer.image). */}}
{{- define "go-taas.image" -}}
{{- $tag := .image.tag | default .root.Values.image.tag | default (printf "v%s" .root.Chart.AppVersion) -}}
{{- $repository := printf "%s/%s" .root.Values.image.repository .name -}}
{{- if .root.Values.image.registry -}}
{{- printf "%s/%s:%s" .root.Values.image.registry $repository $tag -}}
{{- else -}}
{{- printf "%s:%s" $repository $tag -}}
{{- end -}}
{{- end -}}

{{/* ---- credentials ----------------------------------------------------- */}}

{{/* Name of the Secret carrying the credential material. */}}
{{- define "go-taas.secretName" -}}
{{- default (printf "%s-secrets" (include "go-taas.fullname" .)) .Values.secrets.existingSecret -}}
{{- end -}}

{{/*
Generated credential value. The live Secret is looked up first so an upgrade
or rollback never rotates a password the running services already use; when
the Secret (or the key) does not exist yet a random value is generated.
Call with (dict "root" $ "key" "postgresql-password").
*/}}
{{- define "go-taas.generatedSecret" -}}
{{- $name := include "go-taas.secretName" .root -}}
{{- $existing := lookup "v1" "Secret" .root.Release.Namespace $name -}}
{{- $value := "" -}}
{{- if and $existing $existing.data (hasKey $existing.data .key) -}}
{{- $value = (index $existing.data .key | b64dec) -}}
{{- end -}}
{{- if $value -}}
{{- $value -}}
{{- else -}}
{{- randAlphaNum 32 -}}
{{- end -}}
{{- end -}}

{{/* PostgreSQL password: configured, or generated for an in-cluster instance. */}}
{{- define "go-taas.postgresql.password" -}}
{{- $dep := .Values.dependencies.postgresql -}}
{{- if eq $dep.mode "external" -}}
{{- if and (not $dep.external.password) (not .Values.secrets.existingSecret) -}}
{{- fail "go-taas: dependencies.postgresql.mode=external needs dependencies.postgresql.external.password, or secrets.existingSecret providing the postgresql-password key" -}}
{{- end -}}
{{- $dep.external.password -}}
{{- else -}}
{{- $password := $dep.internal.auth.password -}}
{{- if $password -}}
{{- $password -}}
{{- else -}}
{{- include "go-taas.generatedSecret" (dict "root" . "key" "postgresql-password") -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* Redis password: "" whenever the instance runs without authentication. */}}
{{- define "go-taas.redis.password" -}}
{{- $dep := .Values.dependencies.redis -}}
{{- if eq $dep.mode "external" -}}
{{- $dep.external.password -}}
{{- else if $dep.internal.auth.enabled -}}
{{- $password := $dep.internal.auth.password -}}
{{- if $password -}}
{{- $password -}}
{{- else -}}
{{- include "go-taas.generatedSecret" (dict "root" . "key" "redis-password") -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* Keycloak admin password: configured, or generated for an in-cluster instance. */}}
{{- define "go-taas.keycloak.adminPassword" -}}
{{- $dep := .Values.dependencies.keycloak -}}
{{- if eq $dep.mode "external" -}}
{{- $dep.external.clientSecret -}}
{{- else -}}
{{- $password := $dep.internal.adminPassword -}}
{{- if $password -}}
{{- $password -}}
{{- else -}}
{{- include "go-taas.generatedSecret" (dict "root" . "key" "keycloak-admin-password") -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* Webhook secret-at-rest master key: configured, or generated. */}}
{{- define "go-taas.webhookEncryptionKey" -}}
{{- $key := .Values.secrets.webhookSecretEncryptionKey -}}
{{- if $key -}}
{{- $key -}}
{{- else -}}
{{- include "go-taas.generatedSecret" (dict "root" . "key" "webhook-secret-encryption-key") -}}
{{- end -}}
{{- end -}}

{{/* Provider key-at-rest master key: configured, or generated. */}}
{{- define "go-taas.providerEncryptionKey" -}}
{{- $key := .Values.secrets.providerSecretEncryptionKey -}}
{{- if $key -}}
{{- $key -}}
{{- else -}}
{{- include "go-taas.generatedSecret" (dict "root" . "key" "provider-secret-encryption-key") -}}
{{- end -}}
{{- end -}}

{{/*
The cluster name the Wasm filter dispatches to.

Envoy requires a configured cluster for a Wasm HTTP call, and Envoy Gateway
creates exactly one cluster per HTTPRoute rule, named
`httproute/<ns>/<route>/rule/<index>`. Deriving it here keeps the policy and
the route in step: they are rendered together.
*/}}
{{- define "go-taas.gatewayPluginCluster" -}}
{{- printf "httproute/%s/%s-internal-api/rule/0" .Release.Namespace (include "go-taas.fullname" .) -}}
{{- end -}}

{{/* URL the EnvoyExtensionPolicy fetches the Wasm artifact from. */}}
{{- define "go-taas.gatewayPluginArtifactURL" -}}
{{- $port := .Values.taasServer.service.monitor.port | default 9092 -}}
{{- printf "http://%s-server.%s.svc.cluster.local:%v/internal/gateway/plugin.wasm" (include "go-taas.fullname" .) .Release.Namespace $port -}}
{{- end -}}

{{/*
Durations arrive as Go strings ("30s", "500ms") because that is what the
server's config parser expects; the filter's config wants milliseconds.
*/}}
{{- define "go-taas.gatewayPluginMillis" -}}
{{- $raw := .value -}}
{{- $fallback := .fallback -}}
{{- if hasSuffix "ms" $raw -}}
{{- trimSuffix "ms" $raw -}}
{{- else if hasSuffix "s" $raw -}}
{{- mul (atoi (trimSuffix "s" $raw)) 1000 -}}
{{- else if hasSuffix "m" $raw -}}
{{- mul (atoi (trimSuffix "m" $raw)) 60000 -}}
{{- else -}}
{{- $fallback -}}
{{- end -}}
{{- end -}}

{{/* Data-plane gateway plugin token: configured, or generated. */}}
{{- define "go-taas.gatewayPluginToken" -}}
{{- $token := .Values.gatewayPlugin.token -}}
{{- if $token -}}
{{- $token -}}
{{- else -}}
{{- include "go-taas.generatedSecret" (dict "root" . "key" "gateway-plugin-token") -}}
{{- end -}}
{{- end -}}

{{/* ---- configuration rendering ----------------------------------------- */}}

{{/*
Render a nested config map as plain `CONFIG_*` environment variables, e.g.
(dict "prefix" "CONFIG" "values" .Values.taasServer.config) turns
`infer.endpointBaseURL` into CONFIG_INFER_ENDPOINTBASEURL. Only scalar leaves
are supported: the platform reads lists and maps from files, so they belong in
configOverrides.
*/}}
{{/*
Harbor env: the cluster's own registry, used by the image import flow.
An operator sets two obvious values (hostname and project) instead of
editing the generic config map:

  harbor:
    hostname: hub.example.com
    project: taas
    username: admin
    existingSecret: ""      # a Secret holding the password under `harbor-password`
    password: ""            # inline, discouraged; prefer existingSecret

Import is disabled when hostname is empty, and the console then explains
that instead of failing halfway through a transfer.
*/}}
{{- define "go-taas.harbor.env" -}}
{{- with .Values.harbor }}
{{- if .hostname }}
- name: CONFIG_IMAGE_HARBOR_URL
  value: {{ .hostname | quote }}
{{- if .project }}
- name: CONFIG_IMAGE_HARBOR_PROJECT
  value: {{ .project | quote }}
{{- end }}
{{- if .username }}
- name: CONFIG_IMAGE_HARBOR_USERNAME
  value: {{ .username | quote }}
{{- end }}
{{- if .existingSecret }}
- name: CONFIG_IMAGE_HARBOR_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ .existingSecret | quote }}
      key: {{ default "harbor-password" .existingSecretKey | quote }}
{{- else if .password }}
- name: CONFIG_IMAGE_HARBOR_PASSWORD
  value: {{ .password | quote }}
{{- end }}
{{- end }}
{{- end }}
{{- end -}}

{{- define "go-taas.configEnv" -}}
{{- $prefix := .prefix -}}
{{- $indent := .indent -}}
{{- range $key, $value := .values -}}
{{- $entry := printf "%s_%s" $prefix $key -}}
{{- if kindIs "map" $value -}}
{{- include "go-taas.configEnv" (dict "prefix" $entry "values" $value "indent" $indent) -}}
{{- else if kindIs "slice" $value -}}
{{- fail (printf "go-taas: %s is a list and cannot be an environment variable — move it to configOverrides" $entry) -}}
{{- else -}}
{{ printf "- name: %s\n  value: %s" ($entry | replace "." "_" | upper) ($value | toString | quote) | nindent ($indent | int) }}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Render a nested config map as `CONFIG_*` environment variables backed by the
chart Secret, e.g. (dict "prefix" "CONFIG" "secretName" $name
"values" .Values.taasServer.secretConfig). Keys are derived from the variable
name, so CONFIG_IMAGE_HARBOR_PASSWORD is stored as config-image-harbor-password.
*/}}
{{- define "go-taas.secretConfigEnv" -}}
{{- $prefix := .prefix -}}
{{- $secretName := .secretName -}}
{{- $indent := .indent -}}
{{- range $key, $value := .values -}}
{{- $entry := printf "%s_%s" $prefix $key -}}
{{- if kindIs "map" $value -}}
{{- include "go-taas.secretConfigEnv" (dict "prefix" $entry "values" $value "secretName" $secretName "indent" $indent) -}}
{{- else if kindIs "slice" $value -}}
{{- fail (printf "go-taas: %s is a list and cannot be an environment variable — move it to configOverrides" $entry) -}}
{{- else -}}
{{- $envName := $entry | replace "." "_" | upper -}}
{{- $secretKey := $entry | replace "." "_" | lower | replace "_" "-" -}}
{{ printf "- name: %s\n  valueFrom:\n    secretKeyRef:\n      name: %s\n      key: %s" $envName $secretName $secretKey | nindent ($indent | int) }}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Render a nested secret-config map as `stringData` entries of the chart Secret,
using the same key derivation as go-taas.secretConfigEnv.
*/}}
{{- define "go-taas.secretConfigData" -}}
{{- $prefix := .prefix -}}
{{- $indent := .indent -}}
{{- range $key, $value := .values -}}
{{- $entry := printf "%s_%s" $prefix $key -}}
{{- if kindIs "map" $value -}}
{{- include "go-taas.secretConfigData" (dict "prefix" $entry "values" $value "indent" $indent) -}}
{{- else -}}
{{ printf "%s: %s" ($entry | replace "." "_" | lower | replace "_" "-") ($value | toString | quote) | nindent ($indent | int) }}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* ---- dependency connectivity ----------------------------------------- */}}

{{/*
PostgreSQL environment shared by the server and the controller: the
connection either names the instance this chart provisioned or the external
instance from the values file.
*/}}
{{- define "go-taas.postgresql.env" -}}
{{- $root := . -}}
{{- $dep := .Values.dependencies.postgresql -}}
{{- $host := "" -}}
{{- $port := 5432 -}}
{{- $database := "" -}}
{{- $username := "" -}}
{{- $sslMode := "disable" -}}
{{- if eq $dep.mode "external" -}}
{{- $host = required "go-taas: dependencies.postgresql.external.host is required when dependencies.postgresql.mode=external" $dep.external.host -}}
{{- $port = $dep.external.port -}}
{{- $database = required "go-taas: dependencies.postgresql.external.database is required when dependencies.postgresql.mode=external" $dep.external.database -}}
{{- $username = required "go-taas: dependencies.postgresql.external.username is required when dependencies.postgresql.mode=external" $dep.external.username -}}
{{- $sslMode = $dep.external.sslMode -}}
{{- else -}}
{{- $host = include "go-taas.postgresql.name" $root -}}
{{- $port = 5432 -}}
{{- $database = required "go-taas: dependencies.postgresql.internal.auth.database is required" $dep.internal.auth.database -}}
{{- $username = required "go-taas: dependencies.postgresql.internal.auth.username is required" $dep.internal.auth.username -}}
{{- end -}}
- name: CONFIG_DB_MASTER_HOST
  value: {{ $host | quote }}
- name: CONFIG_DB_MASTER_PORT
  value: {{ $port | toString | quote }}
- name: CONFIG_DB_MASTER_DBNAME
  value: {{ $database | quote }}
- name: CONFIG_DB_MASTER_USER
  value: {{ $username | quote }}
- name: CONFIG_DB_MASTER_SSLMODE
  value: {{ $sslMode | quote }}
- name: CONFIG_DB_MASTER_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ include "go-taas.secretName" $root }}
      key: postgresql-password
- name: CONFIG_DB_MASTER_MAXOPENCONNS
  value: {{ $dep.maxOpenConns | toString | quote }}
- name: CONFIG_DB_MASTER_MAXIDLECONNS
  value: {{ $dep.maxIdleConns | toString | quote }}
- name: CONFIG_DB_MASTER_CONNMAXLIFETIME
  value: {{ $dep.connMaxLifetime | quote }}
- name: CONFIG_DB_MASTER_DEBUG
  value: {{ $dep.debug | toString | quote }}
{{- end -}}

{{/* Redis environment (the server's cache, the data-plane verdict store). */}}
{{- define "go-taas.redis.env" -}}
{{- $root := . -}}
{{- $dep := .Values.dependencies.redis -}}
{{- $host := "" -}}
{{- $port := 6379 -}}
{{- if eq $dep.mode "external" -}}
{{- $host = required "go-taas: dependencies.redis.external.host is required when dependencies.redis.mode=external" $dep.external.host -}}
{{- $port = $dep.external.port -}}
{{- else -}}
{{- $host = include "go-taas.redis.name" $root -}}
{{- $port = 6379 -}}
{{- end -}}
- name: CONFIG_REDIS_HOST
  value: {{ $host | quote }}
- name: CONFIG_REDIS_PORT
  value: {{ $port | toString | quote }}
- name: CONFIG_REDIS_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ include "go-taas.secretName" $root }}
      key: redis-password
- name: CONFIG_REDIS_DB
  value: {{ $dep.database | toString | quote }}
- name: CONFIG_REDIS_MAXIDLE
  value: {{ $dep.maxIdle | toString | quote }}
- name: CONFIG_REDIS_MAXACTIVE
  value: {{ $dep.maxActive | toString | quote }}
- name: CONFIG_REDIS_IDLETIMEOUT
  value: {{ $dep.idleTimeout | quote }}
{{- end -}}

{{/* Message-queue environment shared by the server and the controller. */}}
{{- define "go-taas.mq.env" -}}
{{- $root := . -}}
{{- $dep := .Values.dependencies.rabbitmq -}}
{{- $url := "" -}}
{{- if eq $dep.mode "external" -}}
{{- $url = required "go-taas: dependencies.rabbitmq.external.url is required when dependencies.rabbitmq.mode=external" $dep.external.url -}}
{{- else -}}
{{- $url = printf "amqp://guest:guest@%s:%v/" (include "go-taas.rabbitmq.name" $root) $dep.internal.port -}}
{{- end -}}
- name: CONFIG_MQ_DRIVER
  value: {{ $dep.driver | quote }}
- name: CONFIG_MQ_URL
  value: {{ $url | quote }}
- name: CONFIG_MQ_NAMESPACE
  value: {{ $dep.namespace | quote }}
{{- end -}}

{{/* First-boot SSO seed environment (auth.seedSso). Points the seed at the
bundled Keycloak (internal) or the configured external IdP. */}}
{{- define "go-taas.sso.env" -}}
{{- $root := . -}}
{{- $dep := .Values.dependencies.keycloak -}}
{{- $issuer := "" -}}
{{- $clientId := "go-taas-console" -}}
{{- $clientSecret := "go-taas-console-secret" -}}
{{- $redirectUri := "http://localhost:9091/api/v1/auth/sso/*" -}}
{{- /* The seed is off by default: the platform's own account store
       (the seeded local admin) is enough to sign in. Set
       dependencies.keycloak.mode to internal/external to seed an IdP. */ -}}
{{- if eq $dep.mode "disabled" -}}
- name: CONFIG_AUTH_SEEDSSO_ENABLED
  value: "false"
{{- $adminLocal := "Pass4admin" }}
{{- with (.Values.auth).seedSso }}
{{- if .adminLocalPassword }}{{ $adminLocal = .adminLocalPassword }}{{ end }}
{{- end }}
- name: CONFIG_AUTH_SEEDSSO_ADMINLOCALPASSWORD
  value: {{ $adminLocal | quote }}
{{- else -}}
{{- if eq $dep.mode "external" -}}
{{- $issuer = required "go-taas: dependencies.keycloak.external.issuer is required when dependencies.keycloak.mode=external" $dep.external.issuer -}}
{{- $clientId = $dep.external.clientId -}}
{{- $clientSecret = $dep.external.clientSecret -}}
{{- $redirectUri = $dep.external.redirectUri -}}
{{- else -}}
{{- $issuer = printf "http://%s:%v/realms/go-taas" (include "go-taas.keycloak.name" $root) $dep.internal.port -}}
{{- $redirectUri = $dep.internal.redirectUri -}}
{{- end -}}
- name: CONFIG_AUTH_SEEDSSO_ENABLED
  value: "true"
- name: CONFIG_AUTH_SEEDSSO_PROVIDERID
  value: "keycloak"
- name: CONFIG_AUTH_SEEDSSO_ISSUER
  value: {{ $issuer | quote }}
- name: CONFIG_AUTH_SEEDSSO_CLIENTID
  value: {{ $clientId | quote }}
- name: CONFIG_AUTH_SEEDSSO_CLIENTSECRET
  value: {{ $clientSecret | quote }}
- name: CONFIG_AUTH_SEEDSSO_REDIRECTURI
  value: {{ $redirectUri | quote }}
- name: CONFIG_AUTH_SEEDSSO_ADMINUSERNAME
  value: "admin"
- name: CONFIG_AUTH_SEEDSSO_ADMINPASSWORD
  value: "admin"
{{- $adminLocal := "Pass4admin" }}
{{- with (.Values.auth).seedSso }}
{{- if .adminLocalPassword }}{{ $adminLocal = .adminLocalPassword }}{{ end }}
{{- end }}
- name: CONFIG_AUTH_SEEDSSO_ADMINLOCALPASSWORD
  value: {{ $adminLocal | quote }}
{{- end -}}
{{- end -}}

{{/* ---- misc ------------------------------------------------------------ */}}

{{/* True when a raw config-overrides file must be mounted. */}}
{{/*
The chart folds `dataPlane` into the server's configuration file, so an
operator configures the data-plane route in one place. The server owns the
route; these are its install-time parts.
*/}}
{{- define "go-taas.configOverrides" -}}
{{- $overrides := deepCopy (.Values.configOverrides | default dict) -}}
{{- $dp := .Values.dataPlane | default dict -}}
{{- $parent := $dp.parentRef | default dict -}}
{{- $backend := $dp.backend | default dict -}}
{{- $dataPlane := dict
      "enabled" (ternary true false ($dp.enabled | default false))
      "hostname" ($dp.hostname | default "")
      "routeNamespace" .Release.Namespace
      "parentRefName" ($parent.name | default "")
      "parentRefNamespace" ($parent.namespace | default .Release.Namespace)
      "parentRefSection" ($parent.sectionName | default "https")
      "backendName" ($backend.name | default "")
      "backendNamespace" ($backend.namespace | default "")
      "backendPort" ($backend.port | default 80) -}}
{{- $runtime := get $overrides "runtime" | default dict -}}
{{- $_ := set $runtime "dataPlane" $dataPlane -}}
{{- $_ := set $overrides "runtime" $runtime -}}
{{- $gp := .Values.gatewayPlugin | default dict -}}
{{- $gatewayPlugin := dict
      "enabled" (ternary true false ($gp.enabled | default false))
      "token" ""
      "cacheTTL" ($gp.cacheTTL | default "30s")
      "verifyTimeout" ($gp.verifyTimeout | default "500ms")
      "failOpen" (ternary true false ($gp.failOpen | default false))
      "protectedPrefixes" ($gp.protectedPrefixes | default (list "/v1/"))
      "pluginPath" "/usr/local/bin/gateway-plugin.wasm" -}}
{{- $_ := set $overrides "gatewayPlugin" $gatewayPlugin -}}
{{- $monitoring := .Values.monitoring | default dict -}}
{{- if $monitoring.prometheusURL -}}
{{- $rm := get $overrides "resourceMetrics" | default dict -}}
{{- $_ := set $rm "prometheusURL" $monitoring.prometheusURL -}}
{{- $_ := set $rm "gpuUtilizationMetric" ($monitoring.gpuUtilizationMetric | default "DCGM_FI_DEV_GPU_UTIL") -}}
{{- $_ := set $rm "prometheusTimeout" ($monitoring.timeout | default "10s") -}}
{{- $_ := set $overrides "resourceMetrics" $rm -}}
{{- end -}}
{{- toYaml $overrides -}}
{{- end -}}

{{- define "go-taas.hasConfigOverrides" -}}
{{- if or .Values.configOverrides .Values.dataPlane.enabled .Values.monitoring.prometheusURL .Values.gatewayPlugin.enabled -}}true{{- end -}}
{{- end -}}

{{/* Checksum of the config-overrides file, to roll pods when it changes. */}}
{{- define "go-taas.configOverrides.checksum" -}}
{{- include "go-taas.configOverrides" . | sha256sum -}}
{{- end -}}
