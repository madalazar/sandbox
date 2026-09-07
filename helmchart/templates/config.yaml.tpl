apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ include "agentchart.configmapname" . }}
  namespace: {{ include "agentchart.namespace" . }}
data:
  config.yaml: |
{{- .Files.Get "config/config.yaml" | nindent 4 }}
  capabilities.json: |
{{- .Files.Get "config/capabilities.json" | nindent 4 }}
  host-topology.json: |
{{- .Files.Get "config/host-topology.json" | nindent 4 }}
