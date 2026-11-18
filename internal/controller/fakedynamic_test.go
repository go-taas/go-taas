package controller

import (
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// newFakeDynamic is the shared dynamic fake client the reconciler tests build
// their objects in. A dynamic fake client cannot guess the list kind of a
// custom resource, so every resource the reconciler touches is declared here.
func newFakeDynamic(objs ...k8sruntime.Object) *dynamicfake.FakeDynamicClient {
	scheme := k8sruntime.NewScheme()
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		llmSVCGVR:                "LLMInferenceServiceList",
		podGroupGVR:              "PodGroupList",
		resourceClaimTemplateGVR: "ResourceClaimTemplateList",
		resourceClaimGVR:         "ResourceClaimList",
	}, objs...)
}
