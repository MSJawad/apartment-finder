{{/* Labels every object carries. */}}
{{- define "af.labels" -}}
app.kubernetes.io/name: apartment-finder
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels for one component, e.g. "api". Pods carry only these: the
version label changes with every commit, so on a pod it would restart the
database and the routers on every deploy.
*/}}
{{- define "af.selector" -}}
app.kubernetes.io/name: apartment-finder
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end }}

{{/* The app and importer images, tagged with the commit. */}}
{{- define "af.appImage" -}}
{{ .Values.image.app }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end }}
{{- define "af.importerImage" -}}
{{ .Values.image.importer }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end }}

{{/*
Database settings for the app's containers. Kubernetes expands $(DB_PASSWORD)
in DATABASE_URL from the variable before it; the password is hex, so it needs
no escaping in a URL.
*/}}
{{- define "af.databaseEnv" -}}
- name: DB_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ .Values.databaseSecret }}
      key: password
- name: DATABASE_URL
  value: postgres://postgres:$(DB_PASSWORD)@{{ .Release.Name }}-postgres:5432/postgres?sslmode=disable
- name: REGION_CONFIG
  value: {{ .Values.regionConfig }}
- name: LOG_LEVEL
  value: {{ .Values.logLevel }}
{{- end }}

{{/* Locked-down container settings for our own images. */}}
{{- define "af.containerSecurity" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop: [ALL]
{{- end }}

{{/* Pod settings for our own images: never root. */}}
{{- define "af.podSecurity" -}}
runAsNonRoot: true
runAsUser: 65532
runAsGroup: 65532
seccompProfile:
  type: RuntimeDefault
{{- end }}
