{{- define "cluster.backup" -}}
{{- if .Values.backups.enabled }}
backup:
  target: "prefer-standby"
  retentionPolicy: {{ .Values.backups.retentionPolicy }}
  {{- with .Values.backups.volumeSnapshot }}
  volumeSnapshot:
    className: {{ .className }}
  {{- end }}
{{- end }}
{{- end }}
