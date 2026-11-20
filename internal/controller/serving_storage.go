package controller

import (
	"path"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// servingWeights resolves how one serving pod reads its weights. The result
// is a volume, the mount for the engine container, the engine's model
// directory, and any init containers the delivery needs. An empty or unknown
// delivery keeps the platform's historical shared-weights mount, so an older
// control plane and a release without storage.backends behave exactly as
// before.
func (r *k8sReconciler) servingWeights(evt changeEvent) (corev1.Volume, corev1.VolumeMount, string, []corev1.Container) {
	delivery := strings.TrimSpace(evt.Model.Delivery)
	if delivery == "" {
		delivery = "mount"
	}
	switch delivery {
	case "download":
		if evt.Model.ModelURI == "" {
			break
		}
		volume := corev1.Volume{
			Name:         "model",
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		}
		mount := corev1.VolumeMount{Name: "model", MountPath: modelDownloadPath, ReadOnly: true}
		init := corev1.Container{
			Name:  "storage-initializer",
			Image: downloadImage(evt),
			Args:  []string{evt.Model.ModelURI, modelDownloadPath},
			Env:   downloadEnv(evt),
			// The initializer writes into the shared emptyDir; it runs as
			// the image's own user, which the KServe image already sets.
		}
		init.VolumeMounts = []corev1.VolumeMount{{Name: "model", MountPath: modelDownloadPath}}
		return volume, mount, modelDownloadPath, []corev1.Container{init}
	case "cache":
		hostPath := strings.TrimSpace(r.warmupHostPath)
		if hostPath == "" {
			break
		}
		key := storageKeyOf(strings.TrimSpace(evt.Model.ModelURI))
		if key == "" {
			break
		}
		volume := corev1.Volume{
			Name: "weights",
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{Path: hostPath, Type: pathTypeDirectory()},
			},
		}
		mount := corev1.VolumeMount{Name: "weights", MountPath: hostPath, ReadOnly: true}
		return volume, mount, path.Join(hostPath, "models", key), nil
	}

	// mount (and the fallback for an unusable download/cache): the platform's
	// filesystem claim.
	claim := strings.TrimSpace(evt.Model.ClaimName)
	if claim == "" {
		claim = r.weights.PVCName
	}
	mountPath := strings.TrimSpace(evt.Model.MountPath)
	if mountPath == "" {
		mountPath = r.weights.MountPath
	}
	volume := corev1.Volume{
		Name: "weights",
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim},
		},
	}
	mount := corev1.VolumeMount{Name: "weights", MountPath: mountPath, ReadOnly: true}
	return volume, mount, modelDir(mountPath, evt.Model.WeightPath), nil
}

// modelDownloadPath is where the storage initializer lands a model and where
// a download-delivery engine reads it.
const modelDownloadPath = "/models"

// pathTypeDirectory is the hostPath type of the warmup cache.
func pathTypeDirectory() *corev1.HostPathType {
	t := corev1.HostPathDirectory
	return &t
}

// downloadImage returns the storage-initializer image for a download event.
func downloadImage(evt changeEvent) string {
	if image := strings.TrimSpace(evt.Model.DownloadImage); image != "" {
		return image
	}
	return "kserve/storage-initializer:v0.21.0"
}

// downloadEnv projects the object-store coordinates and, when the backend
// names a Secret, its keys into the initializer. The values are never in the
// event: only Secret names and keys are.
func downloadEnv(evt changeEvent) []corev1.EnvVar {
	env := make([]corev1.EnvVar, 0, 8)
	if endpoint := strings.TrimSpace(evt.Model.ObjectEndpoint); endpoint != "" {
		env = append(env,
			corev1.EnvVar{Name: "AWS_ENDPOINT_URL", Value: endpoint},
			corev1.EnvVar{Name: "S3_ENDPOINT", Value: endpoint},
			corev1.EnvVar{Name: "S3_USE_HTTPS", Value: boolString(strings.HasPrefix(endpoint, "https://"))},
		)
	}
	if region := strings.TrimSpace(evt.Model.ObjectRegion); region != "" {
		env = append(env, corev1.EnvVar{Name: "AWS_REGION", Value: region})
	}
	env = append(env, corev1.EnvVar{Name: "S3_USE_VIRTUAL_BUCKET", Value: boolString(!evt.Model.ObjectForcePathStyle)})
	if secret := strings.TrimSpace(evt.Model.CredentialsSecret); secret != "" {
		accessKey := strings.TrimSpace(evt.Model.AccessKeyKey)
		if accessKey == "" {
			accessKey = "accessKey"
		}
		secretKey := strings.TrimSpace(evt.Model.SecretKeyKey)
		if secretKey == "" {
			secretKey = "secretKey"
		}
		env = append(env,
			corev1.EnvVar{Name: "AWS_ACCESS_KEY_ID", ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: secret}, Key: accessKey},
			}},
			corev1.EnvVar{Name: "AWS_SECRET_ACCESS_KEY", ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: secret}, Key: secretKey},
			}},
		)
	}
	return env
}

// boolString renders a bool the way the S3 storage initializer expects it.
func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
