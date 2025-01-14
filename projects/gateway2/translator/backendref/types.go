package backendref

import (
	"fmt"

	"github.com/solo-io/gloo/projects/gateway2/wellknown"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// RefIsHTTPRoute checks if the BackendObjectReference is an HTTPRoute
// Parent routes may delegate to child routes using an HTTPRoute backend reference.
func RefIsHTTPRoute(ref gwv1.BackendObjectReference) bool {
	return (ref.Kind != nil && *ref.Kind == wellknown.HTTPRouteKind) && (ref.Group != nil && *ref.Group == gwv1.GroupName)
}

// ToString returns a string representation of the BackendObjectReference
func ToString(ref gwv1.BackendObjectReference) string {
	var group, kind, namespace string
	if ref.Group != nil {
		group = string(*ref.Group)
	}
	if ref.Kind != nil {
		kind = string(*ref.Kind)
	}
	if ref.Namespace != nil {
		namespace = string(*ref.Namespace)
	}
	return fmt.Sprintf("%s.%s %s/%s", kind, group, namespace, ref.Name)
}
