package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/mq"
)

// Serving scheme values (feature #47, AD3).
const (
	schemePlain  = "plain"
	schemeAIBrix = "aibrix"
)

// SchemeCache holds the latest serving scheme the controller has received
// from the runtime service (AD6). It defaults to plain so a restarted
// controller keeps today's behavior until a broadcast arrives.
type SchemeCache struct {
	mu     sync.RWMutex
	scheme string
}

// NewSchemeCache constructs a scheme cache defaulting to plain.
func NewSchemeCache() *SchemeCache {
	return &SchemeCache{scheme: schemePlain}
}

// Scheme returns the cached scheme.
func (c *SchemeCache) Scheme() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.scheme == "" {
		return schemePlain
	}
	return c.scheme
}

// SetScheme caches a scheme, ignoring unknown values.
func (c *SchemeCache) SetScheme(scheme string) {
	switch scheme {
	case schemePlain, schemeAIBrix:
		c.mu.Lock()
		c.scheme = scheme
		c.mu.Unlock()
	}
}

// runtimeSchemeMessage mirrors the runtime.scheme payload (AD6).
type runtimeSchemeMessage struct {
	Scheme    string `json:"scheme"`
	Revision  int64  `json:"revision"`
	UpdatedAt string `json:"updated_at"`
	UpdatedBy string `json:"updated_by"`
}

// runtimeModelChange mirrors the runtime.models.changes payload (AD5).
type runtimeModelChange struct {
	EventType   string            `json:"event_type"`
	ModelName   string            `json:"model_name"`
	Engine      string            `json:"engine"`
	Replicas    int64             `json:"replicas"`
	ArtifactURL string            `json:"artifact_url"`
	PodSelector map[string]string `json:"pod_selector"`
	EngineArgs  map[string]string `json:"engine_args"`
}

// RuntimeSchemeConsumer subscribes to the runtime.scheme subject and
// caches the latest scheme for the reconciler (AD6).
type RuntimeSchemeConsumer struct {
	client mq.Client
	cache  *SchemeCache
}

// NewRuntimeSchemeConsumer builds a RuntimeSchemeConsumer.
func NewRuntimeSchemeConsumer(client mq.Client, cache *SchemeCache) *RuntimeSchemeConsumer {
	return &RuntimeSchemeConsumer{client: client, cache: cache}
}

// Run implements server.Runner: it subscribes to runtime.scheme until ctx
// is cancelled.
func (c *RuntimeSchemeConsumer) Run(ctx context.Context) error {
	if c.client == nil || c.cache == nil {
		return nil
	}
	return c.client.Subscribe(ctx, mq.DefaultSubjects().RuntimeScheme, c.handle)
}

// handle applies one scheme broadcast.
func (c *RuntimeSchemeConsumer) handle(msg mq.Message) error {
	if c.cache == nil {
		return nil
	}
	var m runtimeSchemeMessage
	if err := json.Unmarshal(msg.Body, &m); err != nil {
		logger.S().Warnw("controller: malformed runtime scheme message, skipping", "err", err)
		return nil
	}
	c.cache.SetScheme(m.Scheme)
	logger.S().Infow("controller: serving scheme updated", "scheme", m.Scheme, "revision", m.Revision)
	return nil
}

// RuntimeModelChangeConsumer subscribes to runtime.models.changes and
// reconciles each registration into an AIBrix ModelClaim (AD5). Every
// write is best-effort: a failure is logged and never blocks serving
// (AD13).
type RuntimeModelChangeConsumer struct {
	client    mq.Client
	dynamic   dynamic.Interface
	namespace string
}

// NewRuntimeModelChangeConsumer builds a RuntimeModelChangeConsumer.
func NewRuntimeModelChangeConsumer(client mq.Client, dyn dynamic.Interface, namespace string) *RuntimeModelChangeConsumer {
	return &RuntimeModelChangeConsumer{client: client, dynamic: dyn, namespace: namespace}
}

// Run implements server.Runner: it subscribes to runtime.models.changes
// until ctx is cancelled.
func (c *RuntimeModelChangeConsumer) Run(ctx context.Context) error {
	if c.client == nil {
		return nil
	}
	return c.client.Subscribe(ctx, mq.DefaultSubjects().RuntimeModelChanges, c.handle)
}

// handle applies one model desired-state change.
func (c *RuntimeModelChangeConsumer) handle(msg mq.Message) error {
	var change runtimeModelChange
	if err := json.Unmarshal(msg.Body, &change); err != nil {
		logger.S().Warnw("controller: malformed runtime model change, skipping", "err", err)
		return nil
	}
	if strings.TrimSpace(change.ModelName) == "" {
		return nil
	}
	switch change.EventType {
	case "delete":
		if err := deleteModelClaim(context.Background(), c.dynamic, c.namespace, change.ModelName); err != nil {
			logger.S().Warnw("controller: delete modelclaim failed", "model", change.ModelName, "err", err)
		}
	case "upsert":
		claim := buildModelClaimFromChange(&change, c.namespace)
		if err := applyModelClaim(context.Background(), c.dynamic, claim); err != nil {
			logger.S().Warnw("controller: apply modelclaim failed", "model", change.ModelName, "err", err)
		}
	}
	return nil
}

// runtimeComponent is one component in the published snapshot.
type runtimeComponent struct {
	ComponentID     string    `json:"component_id"`
	Name            string    `json:"name"`
	Kind            string    `json:"kind"`
	Namespace       string    `json:"namespace"`
	Status          string    `json:"status"`
	ReadyReplicas   int64     `json:"ready_replicas"`
	DesiredReplicas int64     `json:"desired_replicas"`
	LastCheckedAt   time.Time `json:"last_checked_at"`
}

// runtimeModelCondition is one ModelClaim status condition.
type runtimeModelCondition struct {
	Type               string    `json:"type"`
	Status             string    `json:"status"`
	Reason             string    `json:"reason"`
	Message            string    `json:"message"`
	LastTransitionTime time.Time `json:"last_transition_time"`
}

// runtimeModel is one ModelClaim projection in the snapshot.
type runtimeModel struct {
	ModelName       string                  `json:"model_name"`
	Engine          string                  `json:"engine"`
	Replicas        int64                   `json:"replicas"`
	ReadyReplicas   int64                   `json:"ready_replicas"`
	ReadyCandidates int64                   `json:"ready_candidates"`
	Conditions      []runtimeModelCondition `json:"conditions"`
	Status          string                  `json:"status"`
	CreatedAt       time.Time               `json:"created_at"`
	Origin          string                  `json:"origin"`
}

// runtimeSnapshot is the full runtime.health snapshot (AD4).
type runtimeSnapshot struct {
	Scheme         string             `json:"scheme"`
	Availability   string             `json:"availability"`
	RuntimeName    string             `json:"runtime_name"`
	RuntimeVersion string             `json:"runtime_version"`
	ClusterName    string             `json:"cluster_name"`
	Reason         string             `json:"reason"`
	Components     []runtimeComponent `json:"components"`
	Models         []runtimeModel     `json:"models"`
	ReportedAt     time.Time          `json:"reported_at"`
}

// RuntimeCollectorConfig configures the RuntimeCollector.
type RuntimeCollectorConfig struct {
	// Interval is the collection period.
	Interval time.Duration
	// Namespace is the namespace ModelClaims are reconciled into (the
	// controller namespace).
	Namespace string
	// AIBrixNamespace is the namespace of the AIBrix control plane.
	AIBrixNamespace string
	// Gateway is the Envoy AI Gateway Deployment name.
	Gateway string
	// ClusterName is reported in the snapshot.
	ClusterName string
}

// aibrixControlPlaneDeployments are the AIBrix control-plane Deployments
// observed for health (AD4).
var aibrixControlPlaneDeployments = []string{
	"aibrix-controller-manager",
	"aibrix-gateway-plugins",
	"aibrix-gpu-optimizer",
	"aibrix-metadata-service",
	"aibrix-redis-master",
}

// RuntimeCollector lists runtime components and ModelClaims on an
// interval and publishes a full snapshot to runtime.health (AD4). It is
// best-effort telemetry: transient Kubernetes errors are logged, the
// last-known projection is retained, and the loop retries on the next
// tick.
type RuntimeCollector struct {
	clientset kubernetes.Interface
	dynamic   dynamic.Interface
	publisher mq.Client
	cfg       RuntimeCollectorConfig
	scheme    *SchemeCache

	mu             sync.Mutex
	lastComponents []runtimeComponent
	lastModels     []runtimeModel
	published      bool
}

// NewRuntimeCollector builds a RuntimeCollector.
func NewRuntimeCollector(clientset kubernetes.Interface, dyn dynamic.Interface, publisher mq.Client, cfg RuntimeCollectorConfig, scheme *SchemeCache) *RuntimeCollector {
	if cfg.Interval <= 0 {
		cfg.Interval = 30 * time.Second
	}
	if cfg.AIBrixNamespace == "" {
		cfg.AIBrixNamespace = "aibrix-system"
	}
	if cfg.Gateway == "" {
		cfg.Gateway = "envoy-aibrix-system-aibrix-eg"
	}
	if scheme == nil {
		scheme = NewSchemeCache()
	}
	return &RuntimeCollector{clientset: clientset, dynamic: dyn, publisher: publisher, cfg: cfg, scheme: scheme}
}

// Run collects and publishes the full snapshot on the interval until ctx
// is cancelled.
func (c *RuntimeCollector) Run(ctx context.Context) {
	ticker := time.NewTicker(c.cfg.Interval)
	defer ticker.Stop()

	c.collectAndPublish(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.collectAndPublish(ctx)
		}
	}
}

// collectAndPublish builds and publishes one snapshot.
func (c *RuntimeCollector) collectAndPublish(ctx context.Context) {
	scheme := c.scheme.Scheme()
	snap := runtimeSnapshot{Scheme: scheme, RuntimeName: scheme, ReportedAt: time.Now().UTC()}

	if scheme == schemeAIBrix {
		components, version, err := c.collectAIBrixComponents(ctx)
		if err != nil {
			c.publishDegraded(ctx, scheme, err)
			return
		}
		snap.RuntimeVersion = version
		snap.Components = components
		snap.Availability = aggregateAvailability(components)

		models, err := c.collectModels(ctx)
		if err != nil {
			c.publishDegraded(ctx, scheme, err)
			return
		}
		snap.Models = models
	} else {
		components, err := c.collectPlainComponents(ctx)
		if err != nil {
			c.publishDegraded(ctx, scheme, err)
			return
		}
		snap.Components = components
		snap.Availability = aggregateAvailability(components)
		snap.Models = []runtimeModel{}
	}

	c.mu.Lock()
	c.lastComponents = snap.Components
	c.lastModels = snap.Models
	c.published = true
	c.mu.Unlock()
	c.publish(ctx, &snap)
}

// publishDegraded publishes an unavailable/unknown snapshot retaining the
// last-known projection, and never aborts the loop (AD4, D8).
func (c *RuntimeCollector) publishDegraded(ctx context.Context, scheme string, cause error) {
	c.mu.Lock()
	components := c.lastComponents
	models := c.lastModels
	published := c.published
	c.mu.Unlock()

	if components == nil {
		components = []runtimeComponent{}
	}
	if models == nil {
		models = []runtimeModel{}
	}
	availability := "unavailable"
	if !published {
		availability = "unknown"
	}
	snap := runtimeSnapshot{
		Scheme:       scheme,
		Availability: availability,
		RuntimeName:  scheme,
		Reason:       "runtime collection failed: " + cause.Error(),
		Components:   components,
		Models:       models,
		ReportedAt:   time.Now().UTC(),
	}
	logger.S().Warnw("controller: runtime collection failed", "scheme", scheme, "err", cause)
	c.publish(ctx, &snap)
}

// collectAIBrixComponents reads the AIBrix control-plane Deployments and
// the Envoy AI Gateway Deployment (AD4).
func (c *RuntimeCollector) collectAIBrixComponents(ctx context.Context) ([]runtimeComponent, string, error) {
	if c.clientset == nil {
		return nil, "", fmt.Errorf("kubernetes clientset unavailable")
	}
	now := time.Now().UTC()
	components := make([]runtimeComponent, 0, len(aibrixControlPlaneDeployments)+1)
	version := ""
	for _, name := range aibrixControlPlaneDeployments {
		comp := c.deploymentComponent(ctx, c.cfg.AIBrixNamespace, name, "control_plane", now)
		components = append(components, comp)
		if name == "aibrix-controller-manager" {
			version = c.deploymentImageVersion(ctx, c.cfg.AIBrixNamespace, name)
		}
	}
	gateway := c.gatewayComponent(ctx, now)
	components = append(components, gateway)
	return components, version, nil
}

// gatewayComponent reads the Envoy AI Gateway Deployment for the runtime
// health snapshot (AD4). Envoy AI Gateway suffixes its generated
// Deployment name with a hash and may live in the gateway controller's
// own namespace, so when the configured name is not found in the AIBrix
// namespace the collector falls back to a cluster-wide prefix match on
// the configured name. The fallback keeps the composed component ID the
// configured gateway name so the page shows a stable identity.
func (c *RuntimeCollector) gatewayComponent(ctx context.Context, now time.Time) runtimeComponent {
	name := c.cfg.Gateway
	dep, err := c.clientset.AppsV1().Deployments(c.cfg.AIBrixNamespace).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return deploymentToComponent(dep, name, "gateway", now)
	}
	if !apierrors.IsNotFound(err) {
		return unavailableComponent(name, name, "gateway", c.cfg.AIBrixNamespace, now)
	}
	list, listErr := c.clientset.AppsV1().Deployments(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if listErr != nil {
		return unavailableComponent(name, name, "gateway", c.cfg.AIBrixNamespace, now)
	}
	for i := range list.Items {
		d := &list.Items[i]
		if d.Name == name || strings.HasPrefix(d.Name, name+"-") {
			return deploymentToComponent(d, name, "gateway", now)
		}
	}
	return unavailableComponent(name, name, "gateway", c.cfg.AIBrixNamespace, now)
}

// deploymentComponent reads one Deployment and maps its replica state to a
// component status.
func (c *RuntimeCollector) deploymentComponent(ctx context.Context, namespace, name, kind string, now time.Time) runtimeComponent {
	dep, err := c.clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return unavailableComponent(name, name, kind, namespace, now)
	}
	return deploymentToComponent(dep, name, kind, now)
}

// unavailableComponent builds an unavailable component projection.
func unavailableComponent(id, name, kind, namespace string, now time.Time) runtimeComponent {
	return runtimeComponent{
		ComponentID: id, Name: name, Kind: kind, Namespace: namespace,
		Status: "unavailable", LastCheckedAt: now,
	}
}

// deploymentToComponent maps a Deployment's replica state to a component
// projection. The component ID is the configured name so the identity
// stays stable even when the observed Deployment carries a hash suffix.
func deploymentToComponent(dep *appsv1.Deployment, id, kind string, now time.Time) runtimeComponent {
	var desired int64
	if dep.Spec.Replicas != nil {
		desired = int64(*dep.Spec.Replicas)
	}
	ready := int64(dep.Status.ReadyReplicas)
	status := "degraded"
	switch {
	case desired > 0 && ready >= desired:
		status = "ready"
	case ready > 0:
		status = "progressing"
	}
	return runtimeComponent{
		ComponentID: id, Name: dep.Name, Kind: kind, Namespace: dep.Namespace,
		Status: status, ReadyReplicas: ready, DesiredReplicas: desired, LastCheckedAt: now,
	}
}

// deploymentImageVersion returns the image tag of a Deployment's first
// container (best-effort, empty when unavailable).
func (c *RuntimeCollector) deploymentImageVersion(ctx context.Context, namespace, name string) string {
	dep, err := c.clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil || len(dep.Spec.Template.Spec.Containers) == 0 {
		return ""
	}
	ref := dep.Spec.Template.Spec.Containers[0].Image
	if idx := strings.LastIndex(ref, ":"); idx >= 0 && !strings.Contains(ref[idx+1:], "/") {
		return ref[idx+1:]
	}
	return ""
}

// collectPlainComponents returns the fallback component set for the plain
// scheme (AD4, D10).
func (c *RuntimeCollector) collectPlainComponents(ctx context.Context) ([]runtimeComponent, error) {
	now := time.Now().UTC()
	apiStatus := "ready"
	if c.clientset != nil {
		// A lightweight list proves the API server is reachable (the
		// controller is itself the go-taas-controller component).
		if _, err := c.clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{Limit: 1}); err != nil {
			apiStatus = "unavailable"
		}
	}
	return []runtimeComponent{
		{
			ComponentID: "go-taas-controller", Name: "go-taas-controller", Kind: "fallback",
			Namespace: c.cfg.Namespace, Status: "ready", ReadyReplicas: 1, DesiredReplicas: 1,
			LastCheckedAt: now,
		},
		{
			ComponentID: "kubernetes-api", Name: "kubernetes-api", Kind: "fallback",
			Status: apiStatus, LastCheckedAt: now,
		},
	}, nil
}

// collectModels lists and projects ModelClaims (AD5). A list error is
// returned so the caller publishes a degraded snapshot.
func (c *RuntimeCollector) collectModels(ctx context.Context) ([]runtimeModel, error) {
	if c.dynamic == nil {
		return nil, fmt.Errorf("dynamic client unavailable")
	}
	namespaces := []string{c.cfg.Namespace}
	if c.cfg.AIBrixNamespace != "" && c.cfg.AIBrixNamespace != c.cfg.Namespace {
		namespaces = append(namespaces, c.cfg.AIBrixNamespace)
	}
	models := []runtimeModel{}
	for _, ns := range namespaces {
		list, err := c.dynamic.Resource(modelClaimGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		for i := range list.Items {
			models = append(models, projectModelClaim(&list.Items[i]))
		}
	}
	return models, nil
}

// projectModelClaim projects one ModelClaim into a runtime model.
func projectModelClaim(obj *unstructured.Unstructured) runtimeModel {
	model := runtimeModel{
		ModelName: nestedString(obj, "spec", "modelName"),
		Engine:    nestedString(obj, "spec", "engine"),
		Origin:    obj.GetLabels()[mcLabelOrigin],
	}
	if v, ok, _ := unstructured.NestedInt64(obj.Object, "spec", "replicas"); ok {
		model.Replicas = v
	}
	if ts := obj.GetCreationTimestamp(); !ts.IsZero() {
		model.CreatedAt = ts.UTC()
	}
	if v, ok, _ := unstructured.NestedInt64(obj.Object, "status", "candidates"); ok {
		model.ReadyCandidates = v
	}
	if v, ok, _ := unstructured.NestedInt64(obj.Object, "status", "readyReplicas"); ok {
		model.ReadyReplicas = v
	}
	model.Conditions = nestedConditions(obj)
	model.Status = deriveModelStatus(model.Conditions, model.ReadyCandidates)
	return model
}

// nestedConditions extracts status.conditions into the projection shape.
func nestedConditions(obj *unstructured.Unstructured) []runtimeModelCondition {
	raw, ok, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if !ok {
		return nil
	}
	out := make([]runtimeModelCondition, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		cond := runtimeModelCondition{
			Type:    asString(m["type"]),
			Status:  asString(m["status"]),
			Reason:  asString(m["reason"]),
			Message: asString(m["message"]),
		}
		if t, ok := m["lastTransitionTime"].(string); ok {
			if parsed, err := time.Parse(time.RFC3339, t); err == nil {
				cond.LastTransitionTime = parsed.UTC()
			}
		}
		out = append(out, cond)
	}
	return out
}

// deriveModelStatus maps the ModelClaim conditions to a model status.
func deriveModelStatus(conditions []runtimeModelCondition, candidates int64) string {
	hasReady := false
	for _, c := range conditions {
		if c.Type != "Ready" {
			continue
		}
		hasReady = true
		if strings.EqualFold(c.Status, "True") {
			return "ready"
		}
		if strings.EqualFold(c.Status, "False") && c.Reason != "" {
			return "failed"
		}
	}
	if !hasReady && candidates > 0 {
		return "progressing"
	}
	if hasReady {
		return "degraded"
	}
	return "unknown"
}

// aggregateAvailability derives availability from the component set.
func aggregateAvailability(components []runtimeComponent) string {
	if len(components) == 0 {
		return "unknown"
	}
	ready := 0
	for _, c := range components {
		if c.Status == "ready" {
			ready++
		}
	}
	switch {
	case ready == len(components):
		return "available"
	case ready == 0:
		return "unavailable"
	default:
		return "degraded"
	}
}

// publish marshals and publishes the snapshot.
func (c *RuntimeCollector) publish(ctx context.Context, snap *runtimeSnapshot) {
	if c.publisher == nil {
		return
	}
	body, err := json.Marshal(snap)
	if err != nil {
		logger.S().Warnw("controller: marshal runtime snapshot failed", "err", err)
		return
	}
	if err := c.publisher.Publish(ctx, mq.DefaultSubjects().RuntimeHealth, body, nil); err != nil {
		logger.S().Warnw("controller: publish runtime snapshot failed", "err", err)
		return
	}
	logger.S().Infow("controller: runtime snapshot published",
		"scheme", snap.Scheme, "availability", snap.Availability,
		"components", len(snap.Components), "models", len(snap.Models))
}

// nestedString reads a nested string field with a "" fallback.
func nestedString(obj *unstructured.Unstructured, fields ...string) string {
	v, ok, _ := unstructured.NestedString(obj.Object, fields...)
	if !ok {
		return ""
	}
	return v
}

// asString coerces an unstructured value to a string.
func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
