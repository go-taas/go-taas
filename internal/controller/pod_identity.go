package controller

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// serviceIDsByWorkloadName maps the name KServe gives a workload's pods to the
// platform's service id.
//
// A KServe pod cannot carry the platform's service id label: KServe copies an
// LLMInferenceService's labels onto the workload it owns too, and a workload's
// selector is immutable, so a label there has to stay constant for the object's
// whole life. The id therefore lives on the LLMInferenceService, which is
// mutable, and a pod is traced back to its service through the name KServe
// copies from that object onto the pods it owns.
func serviceIDsByWorkloadName(ctx context.Context, dyn dynamic.Interface, namespace string) map[string]string {
	byName := map[string]string{}
	if dyn == nil {
		return byName
	}
	list, err := dyn.Resource(llmSVCGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return byName
	}
	for i := range list.Items {
		id := list.Items[i].GetLabels()[serviceLabelKey]
		if id == "" {
			continue
		}
		byName[list.Items[i].GetName()] = id
	}
	return byName
}

// podServiceID is the service a pod belongs to: the label the platform's own
// workload stamps on it, or the id of the KServe workload KServe names it after.
func podServiceID(pod *corev1.Pod, byWorkloadName map[string]string) string {
	if id := pod.Labels[serviceLabelKey]; id != "" {
		return id
	}
	return byWorkloadName[pod.Labels[kserveWorkloadNameLabel]]
}

// kserveWorkloadNameLabel is the label KServe puts a service's name under on
// every pod it owns.
const kserveWorkloadNameLabel = "app.kubernetes.io/name"

// kservePartOfWorkload is the value of the part-of label on a KServe pod.
const kservePartOfWorkload = "llminferenceservice"

// listServicePods lists the pods of one service, whether the platform's own
// workload or KServe created them.
func listServicePods(ctx context.Context, clientset kubernetes.Interface, dyn dynamic.Interface, namespace, serviceID string) ([]corev1.Pod, error) {
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: serviceLabelKey + "=" + serviceID,
	})
	if err != nil || len(pods.Items) > 0 || dyn == nil {
		return pods.Items, err
	}
	byName := serviceIDsByWorkloadName(ctx, dyn, namespace)
	for name, id := range byName {
		if id != serviceID {
			continue
		}
		owned, listErr := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: kserveWorkloadNameLabel + "=" + name + ",app.kubernetes.io/part-of=" + kservePartOfWorkload,
		})
		if listErr != nil {
			return nil, listErr
		}
		return owned.Items, nil
	}
	return pods.Items, nil
}
