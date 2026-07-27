package auth

import (
	"context"

	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Authorizer decides whether an identity may act on a resource.
type Authorizer interface {
	Authorize(ctx context.Context, id Identity, res Resource) error
}

// SubjectAccessReviewAuthorizer asks the Kubernetes API server.
//
// Using SubjectAccessReview rather than checking a local policy is what makes
// the three authentication paths interchangeable: a token-derived identity and
// a proxy-asserted one are both just a username and a set of groups by the time
// they reach here, so both are evaluated against the cluster's own RBAC.
type SubjectAccessReviewAuthorizer struct {
	client    kubernetes.Interface
	namespace string
}

// NewAuthorizer builds an authorizer scoped to a namespace. An empty namespace
// produces a cluster-scoped check.
func NewAuthorizer(client kubernetes.Interface, namespace string) *SubjectAccessReviewAuthorizer {
	return &SubjectAccessReviewAuthorizer{client: client, namespace: namespace}
}

// Authorize returns nil when the identity holds the permission, a *DeniedError
// when it does not, and any other error when the decision could not be made.
//
// The distinction matters: an API server we cannot reach must fail closed with
// a 500, not be reported to the caller as a denial they could fix by asking for
// a RoleBinding they already have.
func (a *SubjectAccessReviewAuthorizer) Authorize(ctx context.Context, id Identity, res Resource) error {
	if id.IsAnonymous() {
		// system:anonymous is a real Kubernetes user and could in principle be
		// bound to a role. Short-circuiting keeps a misconfigured cluster from
		// granting an unauthenticated caller the ability to drain nodes.
		return &DeniedError{Identity: id, Resource: res, Reason: "no credentials presented"}
	}

	review, err := a.client.AuthorizationV1().SubjectAccessReviews().Create(ctx, &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User:   id.Username,
			UID:    id.UID,
			Groups: id.Groups,
			Extra:  extraToSAR(id.Extra),
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Namespace: a.namespace,
				Group:     res.Group,
				Resource:  res.Resource,
				Verb:      res.Verb,
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return err
	}

	// Denied is checked before Allowed: an authorizer may set both, and an
	// explicit deny is required to win.
	if review.Status.Denied || !review.Status.Allowed {
		return &DeniedError{Identity: id, Resource: res, Reason: review.Status.Reason}
	}
	if review.Status.EvaluationError != "" {
		// Allowed, but the authorizer hit an error reaching that conclusion.
		// Treating a partially-evaluated allow as an allow is how permissions
		// get granted by accident.
		return &DeniedError{Identity: id, Resource: res, Reason: review.Status.EvaluationError}
	}
	return nil
}

func extraToSAR(in map[string][]string) map[string]authorizationv1.ExtraValue {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]authorizationv1.ExtraValue, len(in))
	for k, v := range in {
		out[k] = authorizationv1.ExtraValue(v)
	}
	return out
}

// AllowAll authorizes everything. Used when auth is disabled, and in tests.
type AllowAll struct{}

// Authorize always succeeds.
func (AllowAll) Authorize(context.Context, Identity, Resource) error { return nil }
