# RT Benchmark Experiment Suite

This package combines the existing Helm and Docker Compose RT benchmark variants
into a single application description.

Deployment profiles included:

- Helm profile with `caterpillar_helm`, `cyclictest_helm`, and `stress_helm`
- Compose profile with `caterpillar_compose`, `cyclictest_compose`, and `stress_compose`

Each profile preserves the source component properties and parameter definitions
from the original packages so they can be reviewed and adjusted independently.