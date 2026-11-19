// Command taas-transfer executes one model transfer inside a Kubernetes
// Job. It is deliberately stateless: the Job name and the row id are the
// idempotency key, and progress is reported by the Job's status.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.uber.org/zap/zapcore"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/modelhub"
	"github.com/go-taas/go-taas/pkg/storage"
	"github.com/go-taas/go-taas/pkg/transfer"
)

func main() {
	raw := os.Getenv("TRANSFER_SPEC_JSON")
	if strings.TrimSpace(raw) == "" {
		fmt.Fprintln(os.Stderr, "taas-transfer: TRANSFER_SPEC_JSON is required")
		os.Exit(2)
	}
	var spec transfer.Spec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		fmt.Fprintf(os.Stderr, "taas-transfer: invalid spec: %v\n", err)
		os.Exit(2)
	}
	if err := logger.Init(logger.Config{Level: zapcore.InfoLevel, Encoding: "console"}); err != nil {
		fmt.Fprintf(os.Stderr, "taas-transfer: init logger: %v\n", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	reporter := newProgressReporter(spec.ID)
	reporter.report(ctx, transfer.StateRunning, "", 0, 0)
	progress := func(n int64) { reporter.report(ctx, transfer.StateRunning, "", n, 0) }
	if err := run(ctx, &spec, progress); err != nil {
		reporter.report(ctx, transfer.StateFailed, err.Error(), 0, 0)
		logger.S().Errorw("transfer failed", "transfer", spec.ID, "operation", spec.Operation, "err", err)
		os.Exit(1)
	}
	reporter.report(context.Background(), transfer.StateSucceeded, "", 0, 0)
	logger.S().Infow("transfer succeeded", "transfer", spec.ID, "operation", spec.Operation)
}

// progressReporter mirrors byte progress into the Job's ConfigMap. A
// transfer that does not run in a cluster (a local run, a unit test) simply
// skips the report; the move itself never depends on it.
type progressReporter struct {
	id     string
	client kubernetes.Interface
	ns     string
	name   string
	mu     sync.Mutex
	bytes  int64
	total  int64
}

func newProgressReporter(id string) *progressReporter {
	config, err := rest.InClusterConfig()
	if err != nil {
		return &progressReporter{id: id}
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return &progressReporter{id: id}
	}
	namespace := strings.TrimSpace(os.Getenv("POD_NAMESPACE"))
	if namespace == "" {
		namespace = "taas-jobs"
	}
	return &progressReporter{id: id, client: client, ns: namespace, name: "taas-transfer-progress-" + id}
}

func (p *progressReporter) report(ctx context.Context, state, message string, bytes, total int64) {
	if p.client == nil {
		return
	}
	p.mu.Lock()
	if bytes > 0 {
		p.bytes = bytes
	}
	if total > 0 {
		p.total = total
	}
	payload := transfer.Progress{
		State:      state,
		Message:    message,
		Bytes:      p.bytes,
		TotalBytes: p.total,
		UpdatedAt:  time.Now().UTC(),
	}
	p.mu.Unlock()
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      p.name,
			Namespace: p.ns,
			Labels:    map[string]string{"taas.io/model-transfer": "true", "taas.io/model-transfer-id": p.id},
		},
		Data: map[string]string{"progress.json": string(body)},
	}
	_, err = p.client.CoreV1().ConfigMaps(p.ns).Create(ctx, configMap, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		_, err = p.client.CoreV1().ConfigMaps(p.ns).Update(ctx, configMap, metav1.UpdateOptions{})
	}
	if err != nil {
		logger.S().Debugw("transfer: progress report skipped", "transfer", p.id, "err", err)
	}
}

// run executes one spec.
func run(ctx context.Context, spec *transfer.Spec, progress func(int64)) error {
	switch spec.Operation {
	case transfer.OperationCopy:
		return copyOne(ctx, spec, progress)
	case transfer.OperationDownload:
		return downloadOne(ctx, spec, progress)
	case transfer.OperationDelete:
		return deleteOne(ctx, spec)
	default:
		return fmt.Errorf("unsupported operation %q", spec.Operation)
	}
}

func copyOne(ctx context.Context, spec *transfer.Spec, progress func(int64)) error {
	sourceRemote, err := remoteFor(spec.SourceObjectStore)
	if err != nil {
		return err
	}
	targetRemote, err := remoteFor(spec.TargetObjectStore)
	if err != nil {
		return err
	}
	switch {
	case sourceRemote == nil && targetRemote == nil:
		if strings.TrimSpace(spec.SourcePath) == "" || strings.TrimSpace(spec.TargetPath) == "" {
			return fmt.Errorf("copy needs a source and target path")
		}
		_, err := storage.CopyDir(ctx, spec.SourcePath, spec.TargetPath)
		return err
	case sourceRemote != nil && targetRemote == nil:
		if strings.TrimSpace(spec.TargetPath) == "" {
			return fmt.Errorf("copy needs a target path")
		}
		_, err := sourceRemote.DownloadPrefix(ctx, spec.WeightPath, spec.TargetPath, progress)
		return err
	case sourceRemote == nil && targetRemote != nil:
		if strings.TrimSpace(spec.SourcePath) == "" {
			return fmt.Errorf("copy needs a source path")
		}
		_, err := targetRemote.UploadDir(ctx, spec.SourcePath, spec.WeightPath)
		return err
	default:
		// A cross-bucket copy has no server-side API across buckets; the
		// generic path downloads to a temporary directory and uploads.
		temp, err := os.MkdirTemp("", "taas-transfer-")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(temp) }()
		if _, err := sourceRemote.DownloadPrefix(ctx, spec.WeightPath, temp, progress); err != nil {
			return err
		}
		_, err = targetRemote.UploadDir(ctx, temp, spec.WeightPath)
		return err
	}
}

func downloadOne(ctx context.Context, spec *transfer.Spec, progress func(int64)) error {
	if strings.TrimSpace(spec.HubSource) == "" || strings.TrimSpace(spec.HubModelID) == "" {
		return fmt.Errorf("download needs hub source and model id")
	}
	if strings.TrimSpace(spec.TargetPath) == "" {
		return fmt.Errorf("download needs a target path")
	}
	source, err := modelhub.ParseSource(spec.HubSource)
	if err != nil {
		return err
	}
	downloader := modelhub.NewHTTPDownloader(modelhub.Options{})
	err = downloader.Download(ctx, source, spec.HubModelID, spec.TargetPath)
	if err == nil {
		progress(storage.DirSize(spec.TargetPath))
	}
	return err
}

func deleteOne(ctx context.Context, spec *transfer.Spec) error {
	remote, err := remoteFor(spec.SourceObjectStore)
	if err != nil {
		return err
	}
	if remote != nil {
		_, err := remote.DeletePrefix(ctx, spec.WeightPath)
		return err
	}
	if strings.TrimSpace(spec.SourcePath) == "" {
		return fmt.Errorf("delete needs a source path")
	}
	return os.RemoveAll(spec.SourcePath)
}

// remoteFor builds a client from a serialized object-store reference.
func remoteFor(ref *transfer.ObjectStoreRef) (*storage.RemoteClient, error) {
	if ref == nil {
		return nil, nil
	}
	// The controller exports the credentials through the standard AWS
	// environment variables. A ref without an explicit endpoint is treated
	// as absent.
	if strings.TrimSpace(ref.Endpoint) == "" || strings.TrimSpace(ref.Bucket) == "" {
		return nil, nil
	}
	return storage.NewRemoteClient(*ref)
}
