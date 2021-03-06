/*
Copyright 2020 The Kubernetes Authors All rights reserved.

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

// Package kverify verifies a running kubernetes cluster is healthy
package kverify

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/docker/machine/libmachine/state"
	"github.com/golang/glog"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	kconst "k8s.io/kubernetes/cmd/kubeadm/app/constants"
	"k8s.io/minikube/pkg/minikube/bootstrapper"
	"k8s.io/minikube/pkg/minikube/command"
	"k8s.io/minikube/pkg/minikube/config"
	"k8s.io/minikube/pkg/minikube/cruntime"
	"k8s.io/minikube/pkg/minikube/logs"
)

// minLogCheckTime how long to wait before spamming error logs to console
const minLogCheckTime = 60 * time.Second

const (
	// APIServerWaitKey is the name used in the flags for k8s api server
	APIServerWaitKey = "apiserver"
	// SystemPodsWaitKey is the name used in the flags for pods in the kube system
	SystemPodsWaitKey = "system_pods"
	// DefaultSAWaitKey is the name used in the flags for default service account
	DefaultSAWaitKey = "default_sa"
)

//  vars related to the --wait flag
var (
	// DefaultComponents is map of the the default components to wait for
	DefaultComponents = map[string]bool{APIServerWaitKey: true, SystemPodsWaitKey: true}
	// NoWaitComponents is map of componets to wait for if specified 'none' or 'false'
	NoComponents = map[string]bool{APIServerWaitKey: false, SystemPodsWaitKey: false, DefaultSAWaitKey: false}
	// AllComponents is map for waiting for all components.
	AllComponents = map[string]bool{APIServerWaitKey: true, SystemPodsWaitKey: true, DefaultSAWaitKey: true}
	// DefaultWaitList is list of all default components to wait for. only names to be used for start flags.
	DefaultWaitList = []string{APIServerWaitKey, SystemPodsWaitKey}
	// AllComponentsList list of all valid components keys to wait for. only names to be used used for start flags.
	AllComponentsList = []string{APIServerWaitKey, SystemPodsWaitKey, DefaultSAWaitKey}
)

// ShouldWait will return true if the config says need to wait
func ShouldWait(wcs map[string]bool) bool {
	for _, c := range AllComponentsList {
		if wcs[c] {
			return true
		}
	}
	return false
}

// ExpectedComponentsRunning returns whether or not all expected components are running
func ExpectedComponentsRunning(cs *kubernetes.Clientset) error {
	expected := []string{
		"kube-dns", // coredns
		"etcd",
		"kube-apiserver",
		"kube-controller-manager",
		"kube-proxy",
		"kube-scheduler",
	}

	found := map[string]bool{}

	pods, err := cs.CoreV1().Pods("kube-system").List(meta.ListOptions{})
	if err != nil {
		return err
	}

	for _, pod := range pods.Items {
		glog.Infof("found pod: %s", podStatusMsg(pod))
		if pod.Status.Phase != core.PodRunning {
			continue
		}
		for k, v := range pod.ObjectMeta.Labels {
			if k == "component" || k == "k8s-app" {
				found[v] = true
			}
		}
	}

	missing := []string{}
	for _, e := range expected {
		if !found[e] {
			missing = append(missing, e)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing components: %v", strings.Join(missing, ", "))
	}
	return nil
}

// podStatusMsg returns a human-readable pod status, for generating debug status
func podStatusMsg(pod core.Pod) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%q [%s] %s", pod.ObjectMeta.GetName(), pod.ObjectMeta.GetUID(), pod.Status.Phase))
	for i, c := range pod.Status.Conditions {
		if c.Reason != "" {
			if i == 0 {
				sb.WriteString(": ")
			} else {
				sb.WriteString(" / ")
			}
			sb.WriteString(fmt.Sprintf("%s:%s", c.Type, c.Reason))
		}
		if c.Message != "" {
			sb.WriteString(fmt.Sprintf(" (%s)", c.Message))
		}
	}
	return sb.String()
}

// announceProblems checks for problems, and slows polling down if any are found
func announceProblems(r cruntime.Manager, bs bootstrapper.Bootstrapper, cfg config.ClusterConfig, cr command.Runner) {
	problems := logs.FindProblems(r, bs, cfg, cr)
	if len(problems) > 0 {
		logs.OutputProblems(problems, 5)
		time.Sleep(kconst.APICallRetryInterval * 15)
	}
}

// KubeletStatus checks the kubelet status
func KubeletStatus(cr command.Runner) (state.State, error) {
	glog.Infof("Checking kubelet status ...")
	rr, err := cr.RunCmd(exec.Command("sudo", "systemctl", "is-active", "kubelet"))
	if err != nil {
		// Do not return now, as we still have parsing to do!
		glog.Warningf("%s returned error: %v", rr.Command(), err)
	}
	s := strings.TrimSpace(rr.Stdout.String())
	glog.Infof("kubelet is-active: %s", s)
	switch s {
	case "active":
		return state.Running, nil
	case "inactive":
		return state.Stopped, nil
	case "activating":
		return state.Starting, nil
	}
	return state.Error, nil
}
