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
  cpu-topology-agent.json: |
{{- .Files.Get "config/cpu-topology-agent.json" | nindent 4 }}
