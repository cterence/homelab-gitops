package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/typed/coordination/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Config holds all CLI configuration.
type Config struct {
	DryRun         bool
	Namespace      string
	Concurrency    int
	CapacityMargin float64
	PrometheusURL  string
	OTeleEndpoint  string
	KubeconfigPath string
	ClusterFilter  string
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
	var config *rest.Config
	var err error

	if cfg.KubeconfigPath != "" {
		config, err = clientcmd.BuildConfigFromFlags("", cfg.KubeconfigPath)
	} else {
		// Try in-cluster config first, fall back to kubeconfig
		config, err = rest.InClusterConfig()
		if err != nil {
			if home := homeDir(); home != "" {
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

// Dynamic returns a namespace-scoped dynamic resource interface for the given
// resource and version. The resource string may be a bare name (e.g., "pods")
// for core resources or a fully-qualified name (e.g.,
// "clusters.postgresql.cnpg.io") for CRDs.
func (c *Client) Dynamic(resource, version, namespace string) dynamic.ResourceInterface {
	gvr := parseGVR(resource, version)
	return c.dynamic.Resource(gvr).Namespace(namespace)
}

// parseGVR constructs a GroupVersionResource from a resource string and
// version. The resource string may include a dotted group suffix.
func parseGVR(resource, version string) schema.GroupVersionResource {
	_, gr := schema.ParseResourceArg(resource)
	return schema.GroupVersionResource{
		Group:    gr.Group,
		Version:  version,
		Resource: gr.Resource,
	}
}

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	return os.Getenv("USERPROFILE")
}

// acquireLease tries to acquire a coordination.k8s.io Lease in the given
// namespace. It blocks until the lease is acquired or ctx is cancelled.
// The release function should be called when the work is done to release
// the lease. A background goroutine renews the lease every 15 seconds.
func (c *Client) acquireLease(ctx context.Context, namespace, lockName, identity string) (release func(), err error) {
	leaseClient := c.core.CoordinationV1().Leases(namespace)

	// Ensure the lease namespace exists
	_, err = c.core.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err != nil {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
		if _, err = c.core.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{}); err != nil && !errors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("creating lease namespace: %w", err)
		}
	}

	// Try to acquire the lease by creating it. If it already exists and is
	// held by someone else, wait and retry.
	leaseDuration := 60 * time.Second
	renewDeadline := 30 * time.Second
	retryPeriod := 10 * time.Second

	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for lease: %w", ctx.Err())
		default:
		}

		now := metav1.NewMicroTime(time.Now())
		holder := identity
		lease := &coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{
				Name:      lockName,
				Namespace: namespace,
			},
			Spec: coordinationv1.LeaseSpec{
				HolderIdentity:       &holder,
				LeaseDurationSeconds: ptr(int32(60)),
				AcquireTime:          &now,
				RenewTime:            &now,
			},
		}

		_, createErr := leaseClient.Create(ctx, lease, metav1.CreateOptions{})
		if createErr == nil {
			// Acquired!
			slog.Info("acquired lease", "identity", identity, "lock", lockName)
			done := make(chan struct{})
			go c.renewLease(ctx, leaseClient, namespace, lockName, identity, renewDeadline, retryPeriod, done)
			return func() {
				close(done)
				c.releaseLease(context.Background(), leaseClient, namespace, lockName, identity)
			}, nil
		}

		if !errors.IsAlreadyExists(createErr) {
			return nil, fmt.Errorf("creating lease: %w", createErr)
		}

		// Lease exists — check if it's expired
		existing, getErr := leaseClient.Get(ctx, lockName, metav1.GetOptions{})
		if getErr != nil {
			time.Sleep(retryPeriod)
			continue
		}

		if existing.Spec.HolderIdentity != nil && *existing.Spec.HolderIdentity != identity {
			// Check if the lease has expired
			if existing.Spec.RenewTime != nil {
				expiry := existing.Spec.RenewTime.Add(leaseDuration)
				if time.Now().Before(expiry) {
					slog.Info("lease held by another instance, waiting", "holder", *existing.Spec.HolderIdentity)
					time.Sleep(retryPeriod)
					continue
				}
				slog.Info("lease expired, attempting takeover", "holder", *existing.Spec.HolderIdentity)
			}

			// Try to take over the expired lease
			existing.Spec.HolderIdentity = &holder
			existing.Spec.AcquireTime = &now
			existing.Spec.RenewTime = &now
			_, updateErr := leaseClient.Update(ctx, existing, metav1.UpdateOptions{})
			if updateErr == nil {
				slog.Info("acquired lease (takeover)", "identity", identity, "lock", lockName)
				done := make(chan struct{})
				go c.renewLease(ctx, leaseClient, namespace, lockName, identity, renewDeadline, retryPeriod, done)
				return func() {
					close(done)
					c.releaseLease(context.Background(), leaseClient, namespace, lockName, identity)
				}, nil
			}
			time.Sleep(retryPeriod)
		}
	}
}

func (c *Client) renewLease(ctx context.Context, leaseClient v1.LeaseInterface, namespace, lockName, identity string, renewDeadline, retryPeriod time.Duration, done <-chan struct{}) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := metav1.NewMicroTime(time.Now())
			lease, err := leaseClient.Get(ctx, lockName, metav1.GetOptions{})
			if err != nil {
				slog.Warn("failed to get lease for renewal", "error", err)
				continue
			}
			if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != identity {
				slog.Warn("lost lease", "identity", identity)
				return
			}
			lease.Spec.RenewTime = &now
			if _, err := leaseClient.Update(ctx, lease, metav1.UpdateOptions{}); err != nil {
				slog.Warn("failed to renew lease", "error", err)
			}
		}
	}
}

func (c *Client) releaseLease(ctx context.Context, leaseClient v1.LeaseInterface, namespace, lockName, identity string) {
	lease, err := leaseClient.Get(ctx, lockName, metav1.GetOptions{})
	if err != nil {
		return
	}
	if lease.Spec.HolderIdentity != nil && *lease.Spec.HolderIdentity == identity {
		_ = leaseClient.Delete(ctx, lockName, metav1.DeleteOptions{})
		slog.Info("released lease", "identity", identity, "lock", lockName)
	}
}

func ptr[T any](v T) *T { return &v }
