{{/*
One connector: a ServiceAccount, a Service, a Deployment, a NetworkPolicy
and optionally a PodDisruptionBudget, all named `observability-mcp-<name>`.

  kind "store"    proxy + aggregator + one stock server per signal
  kind "grafana"  proxy + mcp-grafana

The security claim is a shape: every stock server is loopback-only with no
container port and no credential, the proxy holds the pod's only MCP port,
and the pod can connect to exactly the issuer, the store's vmauth (or
Grafana) and cluster DNS.
*/}}
{{- define "observability-mcp.connector" -}}
{{- $root := .root -}}
{{- $full := include "observability-mcp.fullname" . -}}
{{- $isStore := eq .kind "store" -}}
{{- $up := $root.Values.upstreams -}}
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{ $full }}
  labels:
    {{- include "observability-mcp.labels" . | nindent 4 }}
# The pod gets the ONE token it needs, projected below with its own
# audience; the default-audience token is never mounted.
automountServiceAccountToken: false
{{- if $isStore }}
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ printf "%s-aggregator" $full | trunc 63 | trimSuffix "-" }}
  labels:
    {{- include "observability-mcp.labels" . | nindent 4 }}
data:
  config.yaml: |
    {{- include "observability-mcp.aggregatorConfig" . | nindent 4 }}
{{- end }}
---
apiVersion: v1
kind: Service
metadata:
  name: {{ $full }}
  labels:
    {{- include "observability-mcp.labels" . | nindent 4 }}
spec:
  selector:
    {{- include "observability-mcp.selectorLabels" . | nindent 4 }}
  ports:
    - name: http
      port: {{ include "observability-mcp.proxyPort" . }}
      targetPort: http
      protocol: TCP
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ $full }}
  labels:
    {{- include "observability-mcp.labels" . | nindent 4 }}
spec:
  replicas: {{ .replicaCount }}
  # A rollout never takes a pod away before its replacement is ready.
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxUnavailable: 0
      maxSurge: 1
  selector:
    matchLabels:
      {{- include "observability-mcp.selectorLabels" . | nindent 6 }}
  template:
    metadata:
      labels:
        {{- include "observability-mcp.labels" . | nindent 8 }}
      {{- if $isStore }}
      annotations:
        # The aggregator reads its file once at start.
        checksum/aggregator-config: {{ include "observability-mcp.aggregatorConfig" . | sha256sum }}
      {{- end }}
    spec:
      {{- if gt (int .replicaCount) 1 }}
      # Replicas spread over zones and nodes, so one node or zone going away
      # does not take the connector with it. Soft (ScheduleAnyway): a small
      # estate still schedules.
      topologySpreadConstraints:
        - maxSkew: 1
          topologyKey: topology.kubernetes.io/zone
          whenUnsatisfiable: ScheduleAnyway
          labelSelector:
            matchLabels:
              {{- include "observability-mcp.selectorLabels" . | nindent 14 }}
        - maxSkew: 1
          topologyKey: kubernetes.io/hostname
          whenUnsatisfiable: ScheduleAnyway
          labelSelector:
            matchLabels:
              {{- include "observability-mcp.selectorLabels" . | nindent 14 }}
      {{- end }}
      serviceAccountName: {{ $full }}
      automountServiceAccountToken: false
      securityContext:
        runAsNonRoot: true
        seccompProfile:
          type: RuntimeDefault
      containers:
        {{- if $isStore }}
        # The aggregator: one MCP server over the stock servers below. It
        # listens on loopback; its admin port serves /healthz, /readyz and
        # /metrics and nothing of the MCP surface. It is ready only when
        # every allowlisted tool was found, which makes the pod's readiness
        # that too.
        - name: aggregator
          image: {{ include "observability-mcp.aggregatorImage" $root | quote }}
          imagePullPolicy: {{ $root.Values.aggregator.image.pullPolicy }}
          args: ["-config=/etc/mcp-aggregator/config.yaml"]
          ports:
            - name: admin
              containerPort: {{ include "observability-mcp.adminPort" . }}
              protocol: TCP
          livenessProbe:
            httpGet:
              path: /healthz
              port: admin
          readinessProbe:
            httpGet:
              path: /readyz
              port: admin
          securityContext:
            runAsUser: {{ $root.Values.aggregator.runAsUser }}
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: ["ALL"]
          resources:
            {{- toYaml $root.Values.aggregator.resources | nindent 12 }}
          volumeMounts:
            - name: aggregator-config
              mountPath: /etc/mcp-aggregator
              readOnly: true
        {{- range $signal := (include "observability-mcp.storeSignals" .s | fromJsonArray) }}
        {{- $u := index $up (include "observability-mcp.signalUpstream" $signal) }}
        # The stock {{ $signal }} server. Loopback only, no container port, no
        # credential: the only thing that can reach it is the aggregator.
        - name: {{ printf "upstream-%s" $signal }}
          image: {{ include "observability-mcp.image" $u.image | quote }}
          imagePullPolicy: {{ $u.image.pullPolicy }}
          env:
            {{- include (printf "observability-mcp.upstreamEnv.%s" $signal) (dict "root" $root "store" $.s "u" $u) | nindent 12 }}
          securityContext:
            runAsUser: {{ $u.runAsUser }}
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: ["ALL"]
          resources:
            {{- toYaml $u.resources | nindent 12 }}
          volumeMounts:
            - name: tmp
              mountPath: /tmp
        {{- end }}
        {{- else }}
        {{- $u := $up.grafana }}
        # The stock mcp-grafana, dashboards only, read only. Loopback only,
        # no container port. It is given Grafana's address as the proxy's
        # outbound listener and NO token: the proxy's outbound side adds the
        # workload token Grafana accepts. `--allowed-hosts` names the public
        # host the proxy forwards (and its own loopback address): the server
        # refuses any other Host header.
        - name: upstream
          image: {{ include "observability-mcp.image" $u.image | quote }}
          imagePullPolicy: {{ $u.image.pullPolicy }}
          args:
            - --transport=streamable-http
            - --address={{ include "observability-mcp.mcpListen" . }}
            - --enabled-tools={{ join "," $u.enabledTools }}
            - --disable-write
            - --allowed-hosts={{ (urlParse .resourceURL).host }},{{ include "observability-mcp.mcpListen" . }}
            - --usage-stats=disabled
            - --log-level=info
          env:
            - name: GRAFANA_URL
              value: {{ printf "http://%s" (include "observability-mcp.outboundListen" .) | quote }}
          securityContext:
            runAsUser: {{ $u.runAsUser }}
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: ["ALL"]
          resources:
            {{- toYaml $u.resources | nindent 12 }}
          volumeMounts:
            - name: tmp
              mountPath: /tmp
        {{- end }}
        # The resource-proxy. The pod's only MCP port. Its probes are the
        # pod's for the proxy container: the stock servers listen on
        # loopback, which the kubelet cannot reach.
        - name: proxy
          image: {{ include "observability-mcp.image" $root.Values.proxy.image | quote }}
          imagePullPolicy: {{ $root.Values.proxy.image.pullPolicy }}
          env:
            {{- include "observability-mcp.proxyEnv" . | nindent 12 }}
          ports:
            - name: http
              containerPort: {{ include "observability-mcp.proxyPort" . }}
              protocol: TCP
          livenessProbe:
            httpGet:
              path: /healthz
              port: http
          readinessProbe:
            httpGet:
              path: /readyz
              port: http
          securityContext:
            runAsUser: {{ $root.Values.proxy.runAsUser }}
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: ["ALL"]
          resources:
            {{- toYaml $root.Values.proxy.resources | nindent 12 }}
          volumeMounts:
            - name: sa-token
              mountPath: /var/run/observability-mcp
              readOnly: true
            {{- if .caBundle }}
            - name: outbound-ca
              mountPath: {{ dir (include "observability-mcp.outboundCAFile" .) }}
              readOnly: true
            {{- end }}
      volumes:
        - name: tmp
          emptyDir:
            medium: Memory
            sizeLimit: 16Mi
        {{- if $isStore }}
        - name: aggregator-config
          configMap:
            name: {{ printf "%s-aggregator" $full | trunc 63 | trimSuffix "-" }}
        {{- end }}
        - name: sa-token
          projected:
            sources:
              - serviceAccountToken:
                  audience: {{ $root.Values.serviceAccountToken.audience | quote }}
                  expirationSeconds: {{ $root.Values.serviceAccountToken.expirationSeconds }}
                  path: sa-token
        {{- if .caBundle }}
        {{- /* The one key, under a fixed file name: OUTBOUND_CA_FILE names it. */}}
        - name: outbound-ca
          {{- if .caBundle.configMap }}
          configMap:
            name: {{ .caBundle.configMap.name | quote }}
            items:
              - key: {{ .caBundle.configMap.key | quote }}
                path: {{ base (include "observability-mcp.outboundCAFile" .) }}
          {{- else }}
          secret:
            secretName: {{ .caBundle.secret.name | quote }}
            items:
              - key: {{ .caBundle.secret.key | quote }}
                path: {{ base (include "observability-mcp.outboundCAFile" .) }}
          {{- end }}
        {{- end }}
{{- if $root.Values.networkPolicy.enabled }}
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: {{ $full }}
  labels:
    {{- include "observability-mcp.labels" . | nindent 4 }}
spec:
  podSelector:
    matchLabels:
      {{- include "observability-mcp.selectorLabels" . | nindent 6 }}
  policyTypes:
    - Ingress
    - Egress
  ingress:
    # The gateway, on the proxy's one MCP port.
    - from:
        {{- toYaml $root.Values.networkPolicy.ingressFrom | nindent 8 }}
      ports:
        - protocol: TCP
          port: {{ include "observability-mcp.proxyPort" . }}
    {{- if and $isStore $root.Values.networkPolicy.metricsFrom }}
    # Whatever scrapes the aggregator's metrics, on the admin port only.
    - from:
        {{- toYaml $root.Values.networkPolicy.metricsFrom | nindent 8 }}
      ports:
        - protocol: TCP
          port: {{ include "observability-mcp.adminPort" . }}
    {{- end }}
  egress:
    # 1. The issuer: signing keys, and the token exchange.
    - to:
        {{- toYaml $root.Values.networkPolicy.egress.issuer | nindent 8 }}
      ports:
        {{- $issuerPorts := uniq (list (include "observability-mcp.urlPort" $root.Values.issuerURL) (include "observability-mcp.urlPort" .tokenEndpoint)) }}
        {{- range $p := $issuerPorts }}
        - protocol: TCP
          port: {{ $p }}
        {{- end }}
    {{- if $isStore }}
    # 2. The store's vmauth.
    {{- /* The port is the one the POD listens on: `vmauth.podPort` when set, else the URL's (right only when the URL names the pod, or the Service port equals the pod port). */}}
    - to:
        {{- toYaml $root.Values.networkPolicy.egress.vmauth | nindent 8 }}
      ports:
        - protocol: TCP
          port: {{ .podPort | default (include "observability-mcp.urlPort" .target) }}
    {{- else }}
    # 2. Grafana.
    {{- /* The port is the one the POD listens on: `grafana.podPort` when set, else the URL's (the Service port, not the pod's when the Service maps one to the other). */}}
    - to:
        {{- toYaml $root.Values.networkPolicy.egress.grafana | nindent 8 }}
      ports:
        - protocol: TCP
          port: {{ .podPort | default (include "observability-mcp.urlPort" .target) }}
    {{- end }}
    # 3. DNS. With no `networkPolicy.egress.dns` peers this is port 53 to ANY
    # destination (no `to`): the cluster resolver is not a kube-system pod on
    # every cluster, and a rule naming one namespace blocks DNS where it is not.
    {{- with $root.Values.networkPolicy.egress.dns }}
    - to:
        {{- toYaml . | nindent 8 }}
      ports:
        - protocol: UDP
          port: 53
        - protocol: TCP
          port: 53
    {{- else }}
    - ports:
        - protocol: UDP
          port: 53
        - protocol: TCP
          port: 53
    {{- end }}
{{- end }}
{{- if $root.Values.podDisruptionBudget.enabled }}
---
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: {{ $full }}
  labels:
    {{- include "observability-mcp.labels" . | nindent 4 }}
spec:
  minAvailable: {{ $root.Values.podDisruptionBudget.minAvailable }}
  selector:
    matchLabels:
      {{- include "observability-mcp.selectorLabels" . | nindent 6 }}
{{- end }}
{{- end -}}
