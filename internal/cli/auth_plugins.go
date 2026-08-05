package cli

import (
	// Register exec, GCP, Azure, OIDC and other kubeconfig credential plugins
	// so client-go can authenticate the port-forward the same way kubectl does.
	_ "k8s.io/client-go/plugin/pkg/client/auth"
)
