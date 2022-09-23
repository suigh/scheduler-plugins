# Overview

This folder holds the `PowerSaving` plugin implementation based on [Trimaran: Power Consumption Aware Scheduling](https://github.com/kubernetes-sigs/scheduler-plugins/blob/master/kep/TBD).

## Maturity Level

<!-- Check one of the values: Sample, Alpha, Beta, GA -->

- [X] 💡 Sample (for demonstrating and inspiring purpose)
- [ ] 👶 Alpha (used in companies for pilot projects)
- [ ] 👦 Beta (used in companies and developed actively)
- [ ] 👨 Stable (used in companies for production workloads)

## PowerSaving Plugin

`PowerSaving` can get needed data from [Load Watcher](https://github.com/paypal/load-watcher) or [Kepler](https://github.com/sustainable-computing-io/kepler), and this is controlled by `metricProviderType`. If `metricProviderType: Kepler` is configured, `Kepler` will be used, or `Load Watcher` will be used.

By default, `metricProviderType` is `KubernetesMetricsServer` if not set. Now it supports `KubernetesMetricsServer`, `Prometheus`, `SignalFx` and `Kepler`.

- `metricProviderType: KubernetesMetricsServer` use `load-watcher` as a client library to retrieve metrics from Kubernetes metric server.
- `metricProviderType: Prometheus` use `load-watcher` as a client library to retrieve metrics from Prometheus directly.
- `metricProviderType: SignalFx` use `load-watcher` as a client library to retrieve metrics from SignalFx directly.
- `metricProviderType: Kepler` use `Kepler` to retrieve metrics from Kepler directly.

`metricProviderAddress` and `metricProviderToken` should be configured according to `metricProviderType`.

- You can ignore `metricProviderAddress` when using `metricProviderType: KubernetesMetricsServer`
- Configure the prometheus endpoint for `metricProviderAddress` when using `metricProviderType: Prometheus`.
  An example could be `http://prometheus-k8s.monitoring.svc.cluster.local:9090`.

`PowerSaving` uses `load-watcher` in two modes.

1. Using `load-watcher` as a service.
   You can run `load-watcher` service separately to provide real time node resource usage metrics for `PowerSaving` to consume.
   Instructions to build and deploy `load-watcher` can be found [here](https://github.com/paypal/load-watcher/blob/master/README.md).
   In this way, you just need to configure `watcherAddress: http://xxxx.svc.cluster.local:2020` to your `load-watcher` service. You can also deploy `load-watcher` as a service in the same scheduler pod, following the tutorial [here](https://medium.com/paypal-engineering/real-load-aware-scheduling-in-kubernetes-with-trimaran-a8efe14d51e2).

2. Using `load-watcher` as a library to fetch metrics from other providers, such as Prometheus, SignalFx and Kubernetes metric server.
   In this mode, you need to configure three parameters: `metricProviderType`, `metricProviderAddress` and `metricProviderToken` if authentication is needed.

Apart from `watcherAddress`, you can configure the following in `PowerSavingArgs`:

1) `lowCPUThreshold` : Low CPU Utilization % threshold, works when `metricProviderType: Kepler` is not configured.
2) `highCPUThreshold` : High CPU Utilization % threshold, works when `metricProviderType: Kepler` is not configured.

If `metricProviderType: Kepler` is configured, by power consumption metrics from `Kepler`, the node who will get least increased power consumption by running current pod will be used first; if not configured, the nodes whose CPU Utilization are higher than `highCPUThreshold` will be used first, then the nodes whose CPU Utilization are lower than `lowCPUThreshold`, then the other nodes.

By investigation, the power consumption is increased significantly during a certain CPU Utilization (10% ~ 30% by practice), this is why `lowCPUThreshold` and `highCPUThreshold` are involved. When this scheduler cannot get needed power consumption metrics from `Kepler`, these two parameters can help user to save power consumption.

The following is an example config to use `Kepler` to retrieve power consumption metrics to schedule pods:

```yaml
apiVersion: kubescheduler.config.k8s.io/v1beta2
kind: KubeSchedulerConfiguration
leaderElection:
  leaderElect: false
profiles:
- schedulerName: trimaran
  plugins:
    score:
      disabled:
      - name: NodeResourcesBalancedAllocation
      - name: NodeResourcesLeastAllocated
      enabled:
      - name: PowerSaving
  pluginConfig:
    - name: PowerSaving
      args:
        metricProvider:
          type: Kepler
          address: TBD
```

Alternatively, you can use the `load-watcher` to retrieve CPU Utilization metrics and schedule pods with `lowCPUThreshold` and `highCPUThreshold`:

```yaml
apiVersion: kubescheduler.config.k8s.io/v1beta2
kind: KubeSchedulerConfiguration
leaderElection:
  leaderElect: false
profiles:
- schedulerName: trimaran
  plugins:
    score:
      disabled:
      - name: NodeResourcesBalancedAllocation
      - name: NodeResourcesLeastAllocated
      enabled:
      - name: PowerSaving
  pluginConfig:
    - name: PowerSaving
      args:
        lowCPUThreshold: 10
        highCPUThreshold: 30
        metricProvider:
          type: Prometheus
          address: http://prometheus-k8s.monitoring.svc.cluster.local:9090
```
