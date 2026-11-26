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
	reporter.report(ctx, "", transfer.StateRunning, "", 0, 0)
	progress := func(phase string, n, total int64) {
		reporter.report(ctx, phase, transfer.StateRunning, "", n, total)
	}
	if err := run(ctx, &spec, progress); err != nil {
		reporter.report(ctx, "", transfer.StateFailed, err.Error(), 0, 0)
		logger.S().Errorw("transfer failed", "transfer", spec.ID, "operation", spec.Operation, "err", err)
		os.Exit(1)
	}
	reporter.report(context.Background(), "", transfer.StateSucceeded, "", 0, 0)
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
	phase  string
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

func (p *progressReporter) report(ctx context.Context, phase, state, message string, bytes, total int64) {
	if p.client == nil {
		return
	}
	p.mu.Lock()
	// A phase is sticky: the failure and success reports carry no phase of
	// their own, but the row should still say which phase ended the job.
	if phase != "" {
		if phase != p.phase {
			// Bytes belong to a phase: the upload restarts at zero after a
			// completed download, so the console does not sit at 99.5%.
			p.bytes = 0
		}
		p.phase = phase
	}
	if bytes > 0 {
		p.bytes = bytes
	}
	if total > 0 {
		p.total = total
	}
	payload := transfer.Progress{
		State:      state,
		Message:    message,
		Phase:      p.phase,
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
			Labels:    map[string]string{"go-taas.com/model-transfer": "true", "go-taas.com/model-transfer-id": p.id},
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

// progressInterval is how often a running transfer samples its target
// directory for byte progress. It is a variable so tests can shorten it.
var progressInterval = 2 * time.Second

// watchProgress samples dir until stop is called, reporting the bytes written
// so far. It gives a running Job continuous progress instead of one sample at
// the end.
func watchProgress(dir string, total int64, progress func(int64, int64)) (stop func()) {
	done := make(chan struct{})
	finished := make(chan struct{})
	var once sync.Once
	stop = func() {
		once.Do(func() { close(done) })
		// Wait for the sampler to exit: a sample that lands after the next
		// phase began would report the previous phase again.
		<-finished
	}
	go func() {
		defer close(finished)
		ticker := time.NewTicker(progressInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				progress(storage.DirSize(dir), total)
			}
		}
	}()
	return stop
}

// run executes one spec.
func run(ctx context.Context, spec *transfer.Spec, progress func(string, int64, int64)) error {
	var err error
	switch spec.Operation {
	case transfer.OperationCopy:
		err = copyOne(ctx, spec, progress)
	case transfer.OperationDownload:
		err = downloadOne(ctx, spec, progress)
	case transfer.OperationDelete:
		err = deleteOne(ctx, spec)
	case transfer.OperationMove:
		err = moveOne(ctx, spec, progress)
	case transfer.OperationUpload:
		err = uploadOne(ctx, spec, progress)
	default:
		return fmt.Errorf("unsupported operation %q", spec.Operation)
	}
	if err != nil {
		return err
	}
	if err := adoptTargetOwnership(spec); err != nil {
		logger.S().Warnw("transfer: adopt target ownership failed", "transfer", spec.ID, "err", err)
	}
	return nil
}

// moveOne copies the version and only then deletes the source. A failed or
// partial copy leaves the source intact, so the model is never lost.
func moveOne(ctx context.Context, spec *transfer.Spec, progress func(string, int64, int64)) error {
	if err := copyOne(ctx, spec, progress); err != nil {
		return err
	}
	if err := deleteOne(ctx, spec); err != nil {
		return fmt.Errorf("copy succeeded but source cleanup failed: %w", err)
	}
	return nil
}

func copyOne(ctx context.Context, spec *transfer.Spec, progress func(string, int64, int64)) error {
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
		_, err := sourceRemote.DownloadPrefix(ctx, spec.WeightPath, spec.TargetPath, func(n int64) {
			progress(transfer.PhaseDownload, n, 0)
		})
		return err
	case sourceRemote == nil && targetRemote != nil:
		if strings.TrimSpace(spec.SourcePath) == "" {
			return fmt.Errorf("copy needs a source path")
		}
		total := storage.DirSize(spec.SourcePath)
		progress(transfer.PhaseUpload, 0, total)
		_, err := targetRemote.UploadDirWithProgress(ctx, spec.SourcePath, spec.WeightPath, func(n int64) {
			progress(transfer.PhaseUpload, n, total)
		})
		return err
	default:
		// A cross-bucket copy has no server-side API across buckets; the
		// generic path downloads to a temporary directory and uploads.
		temp, err := os.MkdirTemp("", "taas-transfer-")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(temp) }()
		if _, err := sourceRemote.DownloadPrefix(ctx, spec.WeightPath, temp, func(n int64) {
			progress(transfer.PhaseDownload, n, 0)
		}); err != nil {
			return err
		}
		total := storage.DirSize(temp)
		progress(transfer.PhaseUpload, 0, total)
		_, err = targetRemote.UploadDirWithProgress(ctx, temp, spec.WeightPath, func(n int64) {
			progress(transfer.PhaseUpload, n, total)
		})
		return err
	}
}

// newHubDownloader builds the hub client used by a download Job. It is a
// variable so tests can replace the network client.
var newHubDownloader = func() modelhub.Downloader { return modelhub.NewHTTPDownloader(modelhub.Options{}) }

// uploadDirTo moves a downloaded directory into an object-store backend.
var uploadDirTo = func(ctx context.Context, ref *transfer.ObjectStoreRef, dir, key string, progress func(int64)) (int64, error) {
	remote, err := remoteFor(ref)
	if err != nil {
		return 0, err
	}
	return remote.UploadDirWithProgress(ctx, dir, key, progress)
}

func downloadOne(ctx context.Context, spec *transfer.Spec, progress func(string, int64, int64)) error {
	if strings.TrimSpace(spec.HubSource) == "" || strings.TrimSpace(spec.HubModelID) == "" {
		return fmt.Errorf("download needs hub source and model id")
	}
	source, err := modelhub.ParseSource(spec.HubSource)
	if err != nil {
		return err
	}
	downloader := newHubDownloader()
	// Learn the model's size first: without it the console can only show an
	// indeterminate bar for the whole transfer.
	var total int64
	if reporter, ok := downloader.(modelhub.SizeReporter); ok {
		if size, sizeErr := reporter.TotalBytes(ctx, source, spec.HubModelID); sizeErr == nil && size > 0 {
			total = size
		}
	}
	progress(transfer.PhaseDownload, 0, total)
	if objectStoreRefPresent(spec.TargetObjectStore) {
		temp, err := os.MkdirTemp("", "taas-transfer-download-")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(temp) }()
		stop := watchProgress(temp, total, func(n, t int64) {
			progress(transfer.PhaseDownload, n, t)
		})
		defer stop()
		if err := downloader.Download(ctx, source, spec.HubModelID, temp); err != nil {
			return err
		}
		stop()
		// The upload is a phase of its own: its bytes start again from
		// zero and its total is what actually landed on disk, so the
		// console no longer sits at the download's 100%.
		uploadTotal := storage.DirSize(temp)
		progress(transfer.PhaseUpload, 0, uploadTotal)
		written, err := uploadDirTo(ctx, spec.TargetObjectStore, temp, spec.WeightPath, func(n int64) {
			progress(transfer.PhaseUpload, n, uploadTotal)
		})
		if err != nil {
			return err
		}
		progress(transfer.PhaseUpload, written, uploadTotal)
		return nil
	}
	if strings.TrimSpace(spec.TargetPath) == "" {
		return fmt.Errorf("download needs a target path or object store")
	}
	stop := watchProgress(spec.TargetPath, total, func(n, t int64) {
		progress(transfer.PhaseDownload, n, t)
	})
	defer stop()
	if err := downloader.Download(ctx, source, spec.HubModelID, spec.TargetPath); err != nil {
		return err
	}
	stop()
	written := storage.DirSize(spec.TargetPath)
	if total < written {
		total = written
	}
	progress(transfer.PhaseDownload, written, total)
	return nil
}

// objectStoreRefPresent reports whether a ref names a usable object store.
func objectStoreRefPresent(ref *transfer.ObjectStoreRef) bool {
	return ref != nil && strings.TrimSpace(ref.Endpoint) != "" && strings.TrimSpace(ref.Bucket) != ""
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
	if !objectStoreRefPresent(ref) {
		return nil, nil
	}
	return storage.NewRemoteClient(*ref)
}
