/*
Copyright 2022 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

/*
powersaving package provides K8s scheduler plugin for saving cluster's power consumption
It contains plugin for Score extension point.
*/

package powersaving

import (
	"context"
	"fmt"
	"math"

	"github.com/paypal/load-watcher/pkg/watcher"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/scheduler/framework"

	pluginConfig "sigs.k8s.io/scheduler-plugins/apis/config"
	"sigs.k8s.io/scheduler-plugins/apis/config/v1beta2"
	"sigs.k8s.io/scheduler-plugins/pkg/trimaran"
)

const (
	Name = "PowerSaving"
	// Time interval in seconds for each metrics agent ingestion.
	metricsAgentReportingIntervalSeconds = 60
)

var (
	requestsMilliCores   = v1beta2.DefaultRequestsMilliCores
	hostLowCPUThreshold  = v1beta2.DefaultLowCPUThreshold
	hostHighCPUThreshold = v1beta2.DefaultHighCPUThreshold
	requestsMultiplier   float64
	keplerEnabled        = false
)

type PowerSaving struct {
	handle       framework.Handle
	eventHandler *trimaran.PodAssignEventHandler
	collector    *trimaran.Collector
	args         *pluginConfig.PowerSavingArgs
}

func New(obj runtime.Object, handle framework.Handle) (framework.Plugin, error) {
	klog.V(4).InfoS("Creating new instance of the PowerSaving plugin")
	// cast object into plugin arguments object
	args, ok := obj.(*pluginConfig.PowerSavingArgs)
	if !ok {
		return nil, fmt.Errorf("want args to be of type PowerSavingArgs, got %T", obj)
	}
	collector, err := trimaran.NewCollector(&args.TrimaranSpec)
	if err != nil {
		return nil, err
	}

	hostHighCPUThreshold = args.HighCPUThreshold
	hostLowCPUThreshold = args.LowCPUThreshold
	if hostHighCPUThreshold > framework.MaxNodeScore {
		hostHighCPUThreshold = framework.MaxNodeScore
	}
	if hostLowCPUThreshold < framework.MinNodeScore {
		hostLowCPUThreshold = framework.MinNodeScore
	}
	if hostLowCPUThreshold > hostHighCPUThreshold {
		hostLowCPUThreshold = hostHighCPUThreshold
	}

	klog.V(4).InfoS("Using PowerSavingArgs",
		"hostLowCPUThreshold", hostLowCPUThreshold,
		"hostHighCPUThreshold", hostHighCPUThreshold)

	podAssignEventHandler := trimaran.New()
	podAssignEventHandler.AddToHandle(handle)

	pl := &PowerSaving{
		handle:       handle,
		eventHandler: podAssignEventHandler,
		collector:    collector,
		args:         args,
	}
	return pl, nil
}

func (pl *PowerSaving) Name() string {
	return Name
}

func (pl *PowerSaving) Score(ctx context.Context, cycleState *framework.CycleState, pod *v1.Pod, nodeName string) (int64, *framework.Status) {
	score := framework.MinNodeScore
	nodeInfo, err := pl.handle.SnapshotSharedLister().NodeInfos().Get(nodeName)
	if err != nil {
		return score, framework.NewStatus(framework.Error, fmt.Sprintf("getting node %q from Snapshot: %v", nodeName, err))
	}

	// get node metrics
	metrics, allMetrics := pl.collector.GetNodeMetrics(nodeName)
	if metrics == nil {
		klog.InfoS("Failed to get metrics for node; using minimum score", "nodeName", nodeName)
		// Avoid the node by scoring minimum
		return score, nil
		// TODO(aqadeer): If this happens for a long time, fall back to allocation based packing. This could mean maintaining failure state across cycles if scheduler doesn't provide this state

	}

	var nodeCPUUtilPercent float64
	var cpuMetricFound bool
	for _, metric := range metrics {
		if metric.Type == watcher.CPU {
			if metric.Operator == watcher.Average || metric.Operator == watcher.Latest {
				nodeCPUUtilPercent = metric.Value
				cpuMetricFound = true
			}
		}
	}

	if !cpuMetricFound {
		klog.ErrorS(nil, "Cpu metric not found in node metrics", "nodeName", nodeName, "nodeMetrics", metrics)
		return score, nil
	}
	nodeCPUCapMillis := float64(nodeInfo.Node().Status.Capacity.Cpu().MilliValue())
	nodeCPUUtilMillis := (nodeCPUUtilPercent / 100) * nodeCPUCapMillis

	klog.V(6).InfoS("Calculating CPU utilization and capacity", "nodeName", nodeName, "cpuUtilMillis", nodeCPUUtilMillis, "cpuCapMillis", nodeCPUCapMillis)

	var missingCPUUtilMillis int64 = 0
	pl.eventHandler.RLock()
	for _, info := range pl.eventHandler.ScheduledPodsCache[nodeName] {
		// If the time stamp of the scheduled pod is outside fetched metrics window, or it is within metrics reporting interval seconds, we predict util.
		// Note that the second condition doesn't guarantee metrics for that pod are not reported yet as the 0 <= t <= 2*metricsAgentReportingIntervalSeconds
		// t = metricsAgentReportingIntervalSeconds is taken as average case and it doesn't hurt us much if we are
		// counting metrics twice in case actual t is less than metricsAgentReportingIntervalSeconds
		if info.Timestamp.Unix() > allMetrics.Window.End || info.Timestamp.Unix() <= allMetrics.Window.End &&
			(allMetrics.Window.End-info.Timestamp.Unix()) < metricsAgentReportingIntervalSeconds {
			for _, container := range info.Pod.Spec.Containers {
				missingCPUUtilMillis += PredictUtilisation(&container)
			}
			missingCPUUtilMillis += info.Pod.Spec.Overhead.Cpu().MilliValue()
			klog.V(6).InfoS("Missing utilization for pod", "podName", info.Pod.Name, "missingCPUUtilMillis", missingCPUUtilMillis)
		}
	}
	pl.eventHandler.RUnlock()
	klog.V(6).InfoS("Missing utilization for node", "nodeName", nodeName, "missingCPUUtilMillis", missingCPUUtilMillis)

	if nodeCPUCapMillis != 0 {
		nodeCPUUtilPercent = 100 * (nodeCPUUtilMillis + float64(missingCPUUtilMillis)) / nodeCPUCapMillis
	}

	if keplerEnabled {

	} else {
		/* choose nodes by cpu utilization:
		 * 1. bigger than hostHighCPUThreshold
		 * 2. smaller hostLowCPUThreshold
		 * 3. between hostHighCPUThreshold and hostLowCPUThreshold
		 */
		if nodeCPUUtilPercent >= float64(hostHighCPUThreshold) {
			score = int64(math.Round(nodeCPUUtilPercent))
		} else if nodeCPUUtilPercent <= float64(hostLowCPUThreshold) {
			score = int64(math.Round(nodeCPUUtilPercent + float64(hostHighCPUThreshold-hostLowCPUThreshold)))
		} else {
			score = int64(math.Round(nodeCPUUtilPercent - float64(hostLowCPUThreshold)))
		}
	}

	klog.V(6).InfoS("Score for host", "nodeName", nodeName, "score", score)
	return score, framework.NewStatus(framework.Success, "")
}

func (pl *PowerSaving) ScoreExtensions() framework.ScoreExtensions {
	return pl
}

func (pl *PowerSaving) NormalizeScore(ctx context.Context, state *framework.CycleState, pod *v1.Pod, scores framework.NodeScoreList) *framework.Status {
	for _, nodeScore := range scores {
		if nodeScore.Score > framework.MaxNodeScore {
			nodeScore.Score = framework.MaxNodeScore
		} else if nodeScore.Score < framework.MinNodeScore {
			nodeScore.Score = framework.MinNodeScore
		}
	}

	return nil
}

// Predict utilization for a container based on its requests/limits
func PredictUtilisation(container *v1.Container) int64 {
	if _, ok := container.Resources.Limits[v1.ResourceCPU]; ok {
		return container.Resources.Limits.Cpu().MilliValue()
	} else if _, ok := container.Resources.Requests[v1.ResourceCPU]; ok {
		return int64(math.Round(float64(container.Resources.Requests.Cpu().MilliValue()) * requestsMultiplier))
	} else {
		return requestsMilliCores
	}
}
