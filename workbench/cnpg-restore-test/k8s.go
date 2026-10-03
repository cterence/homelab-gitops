package main

import (
	"fmt"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

// Config holds all CLI configuration.
type Config struct {
	DryRun                  bool
	Namespace               string
	Concurrency             int
	CapacityMargin          float64
	CapacityMountpointRegex string
	PrometheusURL           string
	OTeleEndpoint           string
	KubeconfigPath          string
	ClusterFilter           string
}

// Client wraps the Kubernetes dynamic client, corev1 clientset, and rest
// config for exec operations.
type Client struct {
	dynamic    dynamic.Interface
	core       kubernetes.Interface
	restConfig *rest.Config
}

// NewClient creates a Client from kubeconfig (local) or in-cluster config.
func NewClient(cfg Config) (*Client, error) {
	var (
		config *rest.Config
		err    error
	)

	if cfg.KubeconfigPath != "" {
		config, err = clientcmd.BuildConfigFromFlags("", cfg.KubeconfigPath)
	} else {
		// Try in-cluster config first, fall back to kubeconfig
		config, err = rest.InClusterConfig()
		if err != nil {
			if home := homedir.HomeDir(); home != "" {
				config, err = clientcmd.BuildConfigFromFlags("", home+"/.kube/config")
			}
		}
	}

	if err != nil {
		return nil, fmt.Errorf("building kubeconfig: %w", err)
	}

	dyn, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("creating dynamic client: %w", err)
	}

	core, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("creating corev1 clientset: %w", err)
	}

	return &Client{dynamic: dyn, core: core, restConfig: config}, nil
}
